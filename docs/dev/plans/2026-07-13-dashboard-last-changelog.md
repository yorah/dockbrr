# Dashboard Last Applied Changelog: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After an update is applied, the dashboard row keeps showing that update's changelog, readable in a read-only drawer.

**Architecture:** The `updates` row survives an apply with its `changelog_url` / `changelog_text` intact. Only its `status` flips to `applied`, which drops it out of `Updates.ListVisible()` (the `/api/updates` payload). We add a second, read-only path: a store query for the newest `applied` update per service, a `GET /api/updates/last-applied` endpoint, a `useLastApplied()` query, and a `lastApplied` field on dashboard service rows. The Changelog column resolves `update ?? lastApplied`. Nothing about `ListVisible`, status badges, filters, or Apply gating changes.

**Tech Stack:** Go 1.26 + SQLite (`internal/store`, `internal/httpapi` on chi) · React + TypeScript + TanStack Query/Table + Tailwind (`web/`) · vitest + msw for web tests.

## Global Constraints

- Spec: `docs/dev/specs/2026-07-13-dashboard-last-changelog-design.md`.
- `CGO_ENABLED=0` must keep building: no cgo deps (safety invariant 1).
- Changelog markdown renders only via the existing `<Changelog>` component (react-markdown + rehype-sanitize). No `dangerouslySetInnerHTML` (safety invariant 7).
- `Updates.ListVisible()` and `/api/updates` semantics are NOT changed by this work. Applied updates travel on the new endpoint only.
- No new npm or Go dependencies.
- TypeScript typecheck: run `./node_modules/.bin/tsc -b --noEmit` from `web/`: do NOT use `npx tsc` (the rtk proxy reports a false "No errors found").
- Go tests: `go test ./internal/...`. Web tests: `cd web && npm test`.

---

### Task 1: Store: newest applied update per service

**Files:**
- Modify: `internal/store/updates.go` (append a new method after `GetLatestOpenByService`, around line 265)
- Test: `internal/store/updates_test.go` (append)

**Interfaces:**
- Consumes: existing `store.Update` struct, `store.NewUpdates(db)`, test helpers `openImagesStore(t)` and `seedService(t, db)` already in `internal/store/updates_test.go`.
- Produces: `func (u *Updates) ListLastAppliedByService() ([]Update, error)`: one `Update` per service that has at least one `status='applied'` row, each being that service's newest applied row (`detected_at DESC, id DESC`). Services with no applied update contribute nothing.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/updates_test.go`:

```go
func TestUpdatesListLastAppliedByService(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// An older applied update, a newer applied update, and a still-open one.
	oldID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	newID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:b", ToDigest: "sha256:c",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.1", ChangelogText: "# 1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:c", ToDigest: "sha256:d",
		Tag: "1.2", Severity: "minor", Status: "available",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].ID != newID {
		t.Fatalf("got id %d, want newest applied %d (older was %d)", got[0].ID, newID, oldID)
	}
	if got[0].ChangelogText != "# 1.1" || got[0].ChangelogURL != "https://x/1.1" {
		t.Fatalf("changelog not carried: %+v", got[0])
	}
	if got[0].Status != "applied" {
		t.Fatalf("status = %q, want applied", got[0].Status)
	}
}

func TestUpdatesListLastAppliedByServiceExcludesNonApplied(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	for _, st := range []string{"available", "dismissed", "superseded", "rolled_back", "failed"} {
		if _, err := u.Upsert(store.Update{
			ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:" + st,
			Tag: st, Severity: "minor", Status: st,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (%+v)", len(got), got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run ListLastApplied`
Expected: FAIL: compile error `u.ListLastAppliedByService undefined (type *store.Updates has no field or method ListLastAppliedByService)`.

- [ ] **Step 3: Write the implementation**

Append to `internal/store/updates.go` (after `GetLatestOpenByService`):

```go
// ListLastAppliedByService returns, for each service that has ever had an
// update applied, that service's NEWEST applied update, changelog columns
// included. It is the read path behind the dashboard's "last applied
// changelog": an applied update leaves ListVisible, but its cached changelog
// stays on the row (SetStatus never clears it), so the dashboard can keep
// showing it once the pending update is gone.
//
// Only status='applied' rows are considered; a row RecordDrift has flipped back
// to 'available' is therefore (correctly) no longer the last applied one, it is
// pending again and the dashboard renders it via the normal updates list.
func (u *Updates) ListLastAppliedByService() ([]Update, error) {
	rows, err := u.db.Query(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, status, detected_at
		   FROM updates
		  WHERE status='applied'
		    AND id = (SELECT id FROM updates u2
		               WHERE u2.service_id = updates.service_id AND u2.status='applied'
		               ORDER BY u2.detected_at DESC, u2.id DESC LIMIT 1)
		  ORDER BY service_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var up Update
		if err := rows.Scan(
			&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
			&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL,
			&up.ChangelogText, &up.Status, &up.DetectedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, up)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run ListLastApplied -v`
Expected: PASS for both `TestUpdatesListLastAppliedByService` and `TestUpdatesListLastAppliedByServiceExcludesNonApplied`.

- [ ] **Step 5: Run the full package + vet**

Run: `go vet ./internal/store/ && go test ./internal/store/`
Expected: PASS, no vet output.

- [ ] **Step 6: Commit**

```bash
git add internal/store/updates.go internal/store/updates_test.go
git commit -m "feat(store): list newest applied update per service"
```

---

### Task 2: API: GET /api/updates/last-applied

**Files:**
- Modify: `internal/httpapi/updates.go` (add handler next to `handleListUpdates`)
- Modify: `internal/httpapi/server.go:144` (register the route inside the authenticated group)
- Test: `internal/httpapi/updates_test.go` (append)

**Interfaces:**
- Consumes: `Updates.ListLastAppliedByService()` from Task 1; the existing `updateDTO` struct and `writeJSON` / `writeJSONError` helpers in `internal/httpapi`.
- Produces: route `GET /api/updates/last-applied` returning `[]updateDTO` (JSON array, `[]` when empty), each with `status: "applied"`.

Route ordering note: chi matches the static segment `last-applied` before the `{id}` wildcard, so registering it alongside `/api/updates/{id}/preview` is safe regardless of order. Register it directly under the existing `/api/updates` line for readability.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/updates_test.go` (the file already has `updatesDeps` and `seedProjectAndService`; check the existing tests there for how the server is constructed and follow that exact pattern):

```go
func TestListLastAppliedUpdates(t *testing.T) {
	db := openTestStore(t)
	s := newTestServer(t, updatesDeps(db))
	sid := seedProjectAndService(t, s)

	if _, err := s.deps.Updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deps.Updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:b", ToDigest: "sha256:c",
		Tag: "1.1", Severity: "minor", Status: "available",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/updates/last-applied", nil)
	rec := httptest.NewRecorder()
	s.handleListLastApplied(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got []updateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].Status != "applied" || got[0].ChangelogText != "# 1.0" || got[0].ServiceID != sid {
		t.Fatalf("unexpected dto: %+v", got[0])
	}
	if got[0].DetectedAt == "" {
		t.Fatalf("detected_at empty: %+v", got[0])
	}
}
```

If `openTestStore` / `newTestServer` are named differently in `internal/httpapi/testutil_test.go` or in the existing tests of `updates_test.go`, use the names actually present there, do not invent helpers.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestListLastAppliedUpdates`
Expected: FAIL: compile error `s.handleListLastApplied undefined`.

- [ ] **Step 3: Write the handler**

In `internal/httpapi/updates.go`, add directly below `handleListUpdates`:

```go
// handleListLastApplied serves the newest APPLIED update per service. The
// dashboard uses it as the fallback for its Changelog column: once an update is
// applied it drops out of /api/updates (ListVisible), but its cached changelog
// is still worth reading, so the row falls back to this.
func (s *Server) handleListLastApplied(w http.ResponseWriter, r *http.Request) {
	ups, err := s.deps.Updates.ListLastAppliedByService()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]updateDTO, 0, len(ups))
	for _, u := range ups {
		out = append(out, updateDTO{
			ID: u.ID, ServiceID: u.ServiceID, FromDigest: u.FromDigest, ToDigest: u.ToDigest,
			FromVersion: u.FromVersion, ToVersion: u.ToVersion,
			Tag: u.Tag, Severity: u.Severity,
			ChangelogURL: u.ChangelogURL, ChangelogText: u.ChangelogText,
			Status: u.Status, DetectedAt: u.DetectedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 4: Register the route**

In `internal/httpapi/server.go`, inside the authenticated `s.mux.Group(...)` block, directly after the existing `r.Get("/api/updates", s.handleListUpdates)` line:

```go
		r.Get("/api/updates/last-applied", s.handleListLastApplied)
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/httpapi/ -run TestListLastAppliedUpdates -v`
Expected: PASS.

- [ ] **Step 6: Run the full backend suite**

Run: `go vet ./... && go test ./internal/...`
Expected: PASS, no vet output.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/updates.go internal/httpapi/updates_test.go internal/httpapi/server.go
git commit -m "feat(api): GET /api/updates/last-applied"
```

---

### Task 3: Web data layer, useLastApplied + lastApplied on dashboard rows

**Files:**
- Modify: `web/src/api/keys.ts`
- Modify: `web/src/hooks/queries.ts` (next to `useUpdates`)
- Modify: `web/src/hooks/useDashboardRows.ts`
- Modify: `web/src/test/msw.ts` (default handler so app-tree tests don't 404)
- Test: `web/src/hooks/useDashboardRows.test.tsx` (append)

**Interfaces:**
- Consumes: `GET /api/updates/last-applied` from Task 2; the existing `Update` type in `web/src/api/types.ts` (unchanged, the DTO shape is identical).
- Produces:
  - `keys.lastApplied = ["updates", "last-applied"]`: deliberately nested under the `["updates"]` prefix so the existing `invalidateQueries({ queryKey: keys.updates })` calls in `hooks/mutations.ts`, `hooks/useEventStream.ts`, and `components/ApplyPanel.tsx` refresh it too, with no new invalidation wiring.
  - `useLastApplied(): UseQueryResult<Update[]>`
  - `joinRows(projects, updates, filters, lastApplied?: Update[])`: 4th param optional, defaults to `[]`, so existing 3-arg callers and tests keep compiling.
  - `Row` service variant gains `lastApplied?: Update`.
  - `useDashboardRows(filters)` return gains `lastAppliedByService: Map<number, Update>`.

- [ ] **Step 1: Write the failing test**

Append to `web/src/hooks/useDashboardRows.test.tsx` (inside the existing `describe("joinRows", ...)` block):

```tsx
  test("attaches the last applied update to the service row, without affecting filters", () => {
    const lastApplied: Update[] = [
      { id: 42, service_id: 11, from_digest: "sha256:x", to_digest: "sha256:b", from_version: "", to_version: "", tag: "16.1", severity: "minor", changelog_url: "https://x/16.1", changelog_text: "# 16.1", status: "applied", detected_at: "2026-07-01T00:00:00Z" },
    ];
    const rows = joinRows(projects, updates, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false }, lastApplied);
    const db = rows.find((r) => r.kind === "service" && r.service.id === 11);
    expect(db && db.kind === "service" && db.lastApplied?.id).toBe(42);
    // It must not count as a pending update: `db` has no open update, so the
    // onlyUpdates filter still drops it.
    const filtered = joinRows(projects, updates, { onlyUpdates: true, project: "", status: "", search: "", showRemoved: false }, lastApplied);
    expect(filtered.some((r) => r.kind === "service" && r.service.id === 11)).toBe(false);
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/hooks/useDashboardRows.test.tsx`
Expected: FAIL: `db.lastApplied` is `undefined` (`expected undefined to be 42`).

- [ ] **Step 3: Add the query key**

In `web/src/api/keys.ts`, add below the `updates` key:

```ts
  // Nested under the ["updates"] prefix on purpose: every existing
  // invalidateQueries({ queryKey: keys.updates }) call then refreshes this too.
  lastApplied: ["updates", "last-applied"] as const,
```

- [ ] **Step 4: Add the query hook**

In `web/src/hooks/queries.ts`, directly below `useUpdates`:

```ts
export const useLastApplied = () =>
  useQuery({ queryKey: keys.lastApplied, queryFn: () => apiFetch<Update[]>("/api/updates/last-applied") });
```

- [ ] **Step 5: Thread lastApplied through the rows hook**

In `web/src/hooks/useDashboardRows.ts`:

Change the import line to pull in the new hook:

```ts
import { useProjects, useUpdates, useLastApplied } from "./queries";
```

Extend the `Row` type:

```ts
export type Row =
  | { kind: "project"; project: Project }
  | { kind: "service"; project: Project; service: Service; update?: Update; lastApplied?: Update };
```

Change `joinRows` to accept the applied updates (4th param, optional so existing callers keep working). Only the signature, the new map, and the row push change:

```ts
export function joinRows(
  projects: Project[],
  updates: Update[],
  f: FilterState,
  lastApplied: Update[] = [],
): Row[] {
  const byService = new Map<number, Update>();
  for (const u of updates) {
    if (u.status === "available") byService.set(u.service_id, u);
    else if (u.status === "dismissed" && !byService.has(u.service_id)) byService.set(u.service_id, u);
  }
  // Presentational only: the filters below never read it, so an applied update
  // never makes a service look like it has a pending one.
  const appliedByService = new Map<number, Update>();
  for (const u of lastApplied) appliedByService.set(u.service_id, u);
```

…then in the service loop, replace the row push:

```ts
      svcRows.push({
        kind: "service",
        project: p,
        service: s,
        update,
        lastApplied: appliedByService.get(s.id),
      });
```

And in `useDashboardRows`, wire the query in:

```ts
export function useDashboardRows(filters: FilterState) {
  const projects = useProjects();
  const updates = useUpdates();
  const lastApplied = useLastApplied();
  const rows = useMemo(
    () => joinRows(projects.data ?? [], updates.data ?? [], filters, lastApplied.data ?? []),
    [projects.data, updates.data, filters, lastApplied.data],
  );
  const updatesByService = useMemo(() => {
    const m = new Map<number, Update>();
    for (const u of updates.data ?? []) if (openStatuses.has(u.status)) m.set(u.service_id, u);
    return m;
  }, [updates.data]);
  const lastAppliedByService = useMemo(() => {
    const m = new Map<number, Update>();
    for (const u of lastApplied.data ?? []) m.set(u.service_id, u);
    return m;
  }, [lastApplied.data]);
  return {
    rows,
    projects: projects.data ?? [],
    updates: updates.data ?? [],
    updatesByService,
    lastAppliedByService,
    // last-applied is decoration: a failure there must not blank the dashboard,
    // so it is deliberately excluded from isLoading / isError.
    isLoading: projects.isLoading || updates.isLoading,
    isError: projects.isError || updates.isError,
  };
}
```

- [ ] **Step 6: Add the msw default handler**

In `web/src/test/msw.ts`, add to the `handlers` array, below the `/api/updates` entry:

```ts
  http.get("/api/updates/last-applied", () => HttpResponse.json([])),
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/hooks/useDashboardRows.test.tsx`
Expected: PASS, including the pre-existing joinRows tests (they call the 3-arg form).

- [ ] **Step 8: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no output. (Do NOT use `npx tsc`. It reports a false "No errors found".)

- [ ] **Step 9: Commit**

```bash
git add web/src/api/keys.ts web/src/hooks/queries.ts web/src/hooks/useDashboardRows.ts web/src/hooks/useDashboardRows.test.tsx web/src/test/msw.ts
git commit -m "feat(web): fetch last applied update per service"
```

---

### Task 4: ChangelogDrawer: read-only changelog viewer

**Files:**
- Create: `web/src/components/ChangelogDrawer.tsx`
- Test: `web/src/components/ChangelogDrawer.test.tsx`

**Interfaces:**
- Consumes: `Drawer` / `DrawerContent` / `DrawerHeader` / `DrawerTitle` / `DrawerDescription` from `@/components/ui/drawer`, the `<Changelog>` markdown renderer from `@/components/Changelog`, `DigestShort` from `@/components/DigestShort`, and the `Update` / `Service` types: exactly as `ReviewDrawer.tsx` uses them.
- Produces:

```ts
export interface ChangelogDrawerProps {
  update: Update;
  service: Service;
  onClose: () => void;
}
export function ChangelogDrawer(props: ChangelogDrawerProps): JSX.Element;
```

It has NO Apply/Dismiss/Restore controls and issues no mutations: read-only by construction. It is a sibling of `ReviewDrawer`, not a mode of it, so the actionable drawer keeps its current shape.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/ChangelogDrawer.test.tsx`:

```tsx
import { expect, test, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChangelogDrawer } from "./ChangelogDrawer";
import type { Service, Update } from "@/api/types";

const service: Service = {
  id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:c",
  state: "running", pinned: false, drifted: false, healthcheck: false,
  auto_update_enabled: null, check_status: "ok", last_checked: "",
};
const update: Update = {
  id: 42, service_id: 10, from_digest: "sha256:b", to_digest: "sha256:c",
  from_version: "1.27", to_version: "1.28", tag: "1.28", severity: "minor",
  changelog_url: "https://example.test/rel/1.28", changelog_text: "## What's new\n\n- faster",
  status: "applied", detected_at: "2026-07-01T00:00:00Z",
};

test("renders the cached changelog markdown and the external link", () => {
  render(<ChangelogDrawer update={update} service={service} onClose={() => {}} />);
  expect(screen.getByText("What's new")).toBeInTheDocument();
  expect(screen.getByText("faster")).toBeInTheDocument();
  const link = screen.getByRole("link", { name: /view full changelog/i });
  expect(link).toHaveAttribute("href", "https://example.test/rel/1.28");
});

test("exposes no apply or dismiss control", () => {
  render(<ChangelogDrawer update={update} service={service} onClose={() => {}} />);
  expect(screen.queryByRole("button", { name: /^apply$/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^dismiss$/i })).not.toBeInTheDocument();
});

test("says so when the update has no changelog text", () => {
  render(
    <ChangelogDrawer
      update={{ ...update, changelog_text: "", changelog_url: "" }}
      service={service}
      onClose={() => {}}
    />,
  );
  expect(screen.getByText(/no changelog available/i)).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /view full changelog/i })).not.toBeInTheDocument();
});

test("closes on Escape", async () => {
  const onClose = vi.fn();
  render(<ChangelogDrawer update={update} service={service} onClose={onClose} />);
  await userEvent.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalled();
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/components/ChangelogDrawer.test.tsx`
Expected: FAIL: `Failed to resolve import "./ChangelogDrawer"`.

- [ ] **Step 3: Write the component**

Create `web/src/components/ChangelogDrawer.tsx`:

```tsx
import {
  Drawer,
  DrawerContent,
  DrawerHeader,
  DrawerTitle,
  DrawerDescription,
} from "@/components/ui/drawer";
import { Changelog } from "@/components/Changelog";
import { DigestShort } from "@/components/DigestShort";
import type { Service, Update } from "@/api/types";

export interface ChangelogDrawerProps {
  update: Update;
  service: Service;
  onClose: () => void;
}

// Read-only companion to ReviewDrawer: shows an update's cached changelog with
// no Apply/Dismiss controls. The dashboard opens it for the update behind the
// Changelog column, which, once the update has been applied, is the service's
// last applied update rather than a pending one.
export function ChangelogDrawer({ update, service, onClose }: ChangelogDrawerProps) {
  const applied = update.status === "applied";
  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DrawerContent className="w-full max-w-md gap-4 overflow-y-auto sm:max-w-lg">
        <DrawerHeader>
          <DrawerTitle>{service.name}</DrawerTitle>
          <DrawerDescription>
            {applied ? "Last applied update" : "Pending update"} · {update.tag}
          </DrawerDescription>
        </DrawerHeader>

        <section className="flex flex-col gap-1 text-sm">
          {update.from_version && update.to_version && update.from_version !== update.to_version && (
            <span className="text-xs">
              <span>{update.from_version}</span>
              <span aria-hidden="true"> → </span>
              <span className="font-medium">{update.to_version}</span>
            </span>
          )}
          <span className="flex items-center gap-1 text-xs opacity-70">
            <DigestShort digest={update.from_digest} />
            <span aria-hidden="true">→</span>
            <DigestShort digest={update.to_digest} />
          </span>
        </section>

        <section>
          <h3 className="mb-1 text-sm font-medium">Changelog</h3>
          <Changelog markdown={update.changelog_text} />
          {update.changelog_url && (
            <a
              href={update.changelog_url}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-1 inline-block text-xs text-primary hover:underline"
            >
              View full changelog ↗
            </a>
          )}
        </section>
      </DrawerContent>
    </Drawer>
  );
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/components/ChangelogDrawer.test.tsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/ChangelogDrawer.tsx web/src/components/ChangelogDrawer.test.tsx
git commit -m "feat(web): read-only changelog drawer"
```

---

### Task 5: Dashboard Changelog column falls back to the last applied update

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (props + the `changelog` column, currently at lines 285-303)
- Modify: `web/src/routes/dashboard.tsx` (own the drawer state, render `ChangelogDrawer`)
- Test: `web/src/components/DashboardTable.test.tsx` (append)

**Interfaces:**
- Consumes: `Row.lastApplied` (Task 3), `ChangelogDrawer` (Task 4).
- Produces: `DashboardTableProps` gains

```ts
  /** Opens the read-only changelog view for a service's pending or last-applied update. */
  onChangelog: (update: Update, service: Service) => void;
```

Every `DashboardTable` render site must pass it, `web/src/routes/dashboard.tsx` is the only one (verify with `grep -rn "<DashboardTable" web/src`).

Behavior: the cell resolves `update ?? lastApplied`. It renders a button when that update has `changelog_text` OR `changelog_url`; otherwise the existing `EMPTY` dash. When the cell resolved from `lastApplied`, the button is muted and its accessible name says "last applied", so history reads differently from a pending changelog.

- [ ] **Step 1: Write the failing test**

Append to `web/src/components/DashboardTable.test.tsx` (reuse the file's existing `renderDashboardWithRouter` helper and msw `server.use` style):

```tsx
test("changelog column falls back to the last applied update once nothing is pending", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.28",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.get("/api/updates/last-applied", () =>
      HttpResponse.json([
        {
          id: 42,
          service_id: 10,
          from_digest: "sha256:b",
          to_digest: "sha256:c",
          from_version: "1.27",
          to_version: "1.28",
          tag: "1.28",
          severity: "minor",
          changelog_url: "https://example.test/rel/1.28",
          changelog_text: "## What's new\n\n- faster",
          status: "applied",
          detected_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();

  const button = await screen.findByRole("button", { name: /last applied changelog for web/i });
  await userEvent.click(button);

  // The read-only drawer opens with the cached markdown; no Apply control.
  await waitFor(() => expect(screen.getByText("What's new")).toBeInTheDocument());
  expect(screen.queryByRole("button", { name: /^apply$/i })).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/components/DashboardTable.test.tsx`
Expected: FAIL (the new test cannot find a button named "last applied changelog for web": the column still renders the dash placeholder, because the row has no pending update).

- [ ] **Step 3: Extend the table props**

In `web/src/components/DashboardTable.tsx`, add to `DashboardTableProps` (after `onApplied`):

```ts
  /** Opens the read-only changelog view for a service's pending or last-applied update. */
  onChangelog: (update: Update, service: Service) => void;
```

Thread it through the component signature and the columns builder:

```ts
export function DashboardTable({ rows, onReview, updatesByService, onApplied, onChangelog }: DashboardTableProps) {
```

```ts
  const columns = useMemo(
    () => buildColumns(onReview, onApplied, onChangelog),
    [onReview, onApplied, onChangelog],
  );
```

```ts
function buildColumns(
  onReview: DashboardTableProps["onReview"],
  onApplied: DashboardTableProps["onApplied"],
  onChangelog: DashboardTableProps["onChangelog"],
): ColumnDef<Row>[] {
```

- [ ] **Step 4: Rewrite the changelog column**

Replace the whole `changelog` column object in `buildColumns` with:

```tsx
    {
      id: "changelog",
      header: "Changelog",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        // Pending update first; once it is applied it leaves /api/updates, so fall
        // back to the service's last applied update, its changelog is still cached.
        const update = r.update ?? r.lastApplied;
        if (!update || (!update.changelog_text && !update.changelog_url)) return EMPTY;
        const isHistory = update === r.lastApplied && !r.update;
        return (
          <button
            type="button"
            className={cn(
              "hover:underline",
              isHistory ? "text-muted-foreground" : "text-primary",
            )}
            aria-label={
              isHistory
                ? `Last applied changelog for ${r.service.name}`
                : `Changelog for ${r.service.name}`
            }
            onClick={(e) => {
              e.stopPropagation();
              onChangelog(update, r.service);
            }}
          >
            Changelog
          </button>
        );
      },
    },
```

Note: this replaces the old external `<a href={changelog_url}>`. The external link is not lost. It lives inside `ChangelogDrawer` as "View full changelog ↗", and the cell now also works for updates that have text but no URL.

- [ ] **Step 5: Wire the drawer into the dashboard route**

In `web/src/routes/dashboard.tsx`:

Add the import:

```ts
import { ChangelogDrawer } from "@/components/ChangelogDrawer";
```

Add state next to `selected`:

```ts
  const [changelogFor, setChangelogFor] = useState<{ update: Update; service: Service } | null>(null);
```

Pass the handler to the table (inside the existing `<DashboardTable ... />`):

```tsx
          onChangelog={(update, service) => setChangelogFor({ update, service })}
```

Render the drawer next to the existing `ReviewDrawer` block:

```tsx
      {changelogFor && (
        <ChangelogDrawer
          update={changelogFor.update}
          service={changelogFor.service}
          onClose={() => setChangelogFor(null)}
        />
      )}
```

- [ ] **Step 6: Run the web suite**

Run: `cd web && npm test`
Expected: PASS. If a pre-existing DashboardTable test asserted on the old changelog `<a>` link (grep the file for `changelog_url` / `getByRole("link"`), update it to assert the button + drawer instead. The external link now lives in the drawer, and that is the intended behavior change.

- [ ] **Step 7: Typecheck and build**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: typecheck silent; build succeeds.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx web/src/routes/dashboard.tsx
git commit -m "feat(web): dashboard changelog falls back to last applied update"
```

---

### Task 6: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Full check**

Run: `mise run check`
Expected: `go vet` silent, all Go tests pass, all vitest tests pass.

- [ ] **Step 2: Static binary invariant**

Run: `CGO_ENABLED=0 go build ./...`
Expected: succeeds, no output.

- [ ] **Step 3: Drive the real flow**

Invoke the `verify` skill (or run `mise run dev`), then in the UI: apply an update to a service, wait for the job to finish, and confirm the service's dashboard row shows a muted "Changelog" button that opens the read-only drawer with the applied version's notes, and that the row's Status badge still reads Up to date, with the Apply action disabled.

- [ ] **Step 4: Commit any fixes and open the PR**

```bash
git push -u origin HEAD
gh pr create --draft --title "Dashboard: last applied changelog" --body "$(cat <<'EOF'
After an apply, the update leaves ListVisible and the dashboard Changelog
column emptied, even though the changelog text was still cached on the row.

Adds Updates.ListLastAppliedByService + GET /api/updates/last-applied, and
makes the dashboard Changelog column fall back to the service's last applied
update, opening it in a new read-only ChangelogDrawer. ListVisible, status
badges, filters, and Apply gating are unchanged.

Spec: docs/dev/specs/2026-07-13-dashboard-last-changelog-design.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```
