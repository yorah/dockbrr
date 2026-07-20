# Manual "Check for updates" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a user-initiated "Check for updates" button next to the version number in Settings > Application that performs a fresh GitHub check (bypassing the 6h cache) and shows an "up to date" / "vX available" status line.

**Architecture:** Backend gains a force-refresh path on `selfupdate.Checker` (`CheckFresh`) exposed via `?force=true` on the existing `GET /api/updates/self`. Frontend adds a mutation-style hook that calls the forced endpoint and writes the verdict into the shared `keys.selfUpdate` cache, so the sidebar notice and the settings card share one source of truth. The Build card renders a status sub-line under the Version row.

**Tech Stack:** Go 1.26 (CGO-free), chi router, SQLite settings store; React + TypeScript, TanStack Query, Vitest, Tailwind.

## Global Constraints

- `CGO_ENABLED=0` must stay buildable (static binary invariant). No new cgo deps.
- Compose/Docker untouched — this is read-only release checking, no Docker mutation.
- Backend self-update stays best-effort: a GitHub failure never yields a 5xx; it degrades to a stale-cache or `update_available:false` verdict.
- No new apply/link buttons in the Build card (status-only, per spec). The sidebar `UpdateNotice` keeps ownership of the actual update action.
- Frontend: mutations carry the CSRF header + `credentials: include` (already handled by `apiFetch`). This endpoint is a GET, so no CSRF concern; keep using `apiFetch`.
- TS typecheck via `npm run typecheck` (NOT `npx tsc`). `mise run check` runs go vet + go test + vitest.
- No em-dashes in code comments or copy.

Spec: `docs/dev/specs/2026-07-20-manual-check-for-updates-design.md`.

## File Structure

- `internal/selfupdate/checker.go` — refactor `Check` to share a `refresh` helper; add `CheckFresh`.
- `internal/selfupdate/checker_test.go` — tests for `CheckFresh`.
- `internal/httpapi/selfupdate.go` — `handleSelfUpdate` reads `?force=true`.
- `internal/httpapi/selfupdate_test.go` — force-path handler test.
- `web/src/hooks/mutations.ts` — `useCheckForUpdates` hook.
- `web/src/components/settings/ApplicationSettings.tsx` — button + Version sub-line + relative time.
- `web/src/components/settings/ApplicationSettings.test.tsx` — routed fetch stub + new assertions.

---

### Task 1: Backend force-refresh path on the Checker

**Files:**
- Modify: `internal/selfupdate/checker.go` (the `Check` method, ~lines 65-90)
- Test: `internal/selfupdate/checker_test.go`

**Interfaces:**
- Consumes: existing `fetchLatest`, `readCache`, `writeCache`, `result` helpers (unchanged).
- Produces: `func (c *Checker) CheckFresh(ctx context.Context) (Result, error)` — same best-effort contract as `Check` but ignores the TTL and always refetches; `func (c *Checker) Check(...)` behavior unchanged.

- [ ] **Step 1: Write the failing test**

Add to `internal/selfupdate/checker_test.go`:

```go
func TestCheckFreshBypassesYoungCache(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://x/y", &hits)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	if _, err := c.Check(context.Background()); err != nil { // warms a fresh cache
		t.Fatal(err)
	}
	res, err := c.CheckFresh(context.Background()) // must refetch despite young cache
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Errorf("CheckFresh must bypass the young cache; hits=%d, want 2", hits)
	}
	if res.Latest != "v0.5.0" || !res.UpdateAvailable {
		t.Errorf("verdict: %+v", res)
	}
}

func TestCheckFreshErrorServesStaleCache(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	s := newSettings(t)
	seedCache(t, s, "v0.5.0", "https://stale", time.Now().Add(-2*time.Hour).UTC())
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.CheckFresh(context.Background())
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if res.Latest != "v0.5.0" || !res.UpdateAvailable {
		t.Errorf("want stale v0.5.0 served, got %+v", res)
	}
}

func TestCheckFreshErrorNoCache(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.CheckFresh(context.Background())
	if err == nil {
		t.Error("want soft error when no cache and GitHub fails")
	}
	if res.UpdateAvailable {
		t.Errorf("no cache + error must not claim an update: %+v", res)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/ -run TestCheckFresh -v`
Expected: FAIL — `c.CheckFresh undefined (type *selfupdate.Checker has no field or method CheckFresh)`.

- [ ] **Step 3: Refactor `Check` and add `CheckFresh`**

In `internal/selfupdate/checker.go`, replace the current `Check` method (lines ~65-90) with:

```go
// Check returns the latest-release verdict. It serves a cached answer when the
// cache is younger than the TTL, otherwise it refetches. On a GitHub failure it
// falls back to a stale cache when one exists (returning nil error), and only
// returns an error when there is nothing cached to fall back on.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	tag, url, checkedAt, haveCache := c.readCache()
	if haveCache && time.Since(checkedAt) < c.ttl {
		return c.result(tag, url, checkedAt), nil
	}
	return c.refresh(ctx, haveCache, tag, url, checkedAt)
}

// CheckFresh always refetches from GitHub, ignoring the cache TTL. It shares
// Check's best-effort contract: on a GitHub error it serves a stale cache when
// one exists (nil error), and only errors when there is nothing to fall back on.
// Used by the manual "Check for updates" action, which must reflect a
// brand-new release rather than a verdict cached minutes ago.
func (c *Checker) CheckFresh(ctx context.Context) (Result, error) {
	tag, url, checkedAt, haveCache := c.readCache()
	return c.refresh(ctx, haveCache, tag, url, checkedAt)
}

// refresh performs the GitHub fetch, cache-write on success, and stale-cache
// fallback on failure shared by Check and CheckFresh.
func (c *Checker) refresh(ctx context.Context, haveCache bool, tag, url string, checkedAt time.Time) (Result, error) {
	fTag, fURL, err := c.fetchLatest(ctx)
	if err != nil {
		if haveCache {
			// Best-effort: serve stale and leave checked_at untouched so the
			// next request retries GitHub rather than waiting out the TTL.
			logger.Debugf("selfupdate: github fetch failed, serving stale cache: %v", err)
			return c.result(tag, url, checkedAt), nil
		}
		logger.Debugf("selfupdate: github fetch failed, no cache: %v", err)
		return Result{Current: c.current}, err
	}

	now := time.Now().UTC()
	c.writeCache(fTag, fURL, now)
	return c.result(fTag, fURL, now), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/ -v`
Expected: PASS — new `TestCheckFresh*` pass AND all pre-existing `TestCheck*` still pass (the refactor is behavior-preserving).

- [ ] **Step 5: Commit**

```bash
git add internal/selfupdate/checker.go internal/selfupdate/checker_test.go
git commit -m "feat(selfupdate): add CheckFresh force-refresh path"
```

---

### Task 2: Handler honors `?force=true`

**Files:**
- Modify: `internal/httpapi/selfupdate.go` (`handleSelfUpdate`, lines ~14-30)
- Test: `internal/httpapi/selfupdate_test.go`

**Interfaces:**
- Consumes: `Checker.Check` and `Checker.CheckFresh` from Task 1.
- Produces: `GET /api/updates/self?force=true` triggers a live fetch; response body shape is identical to the non-forced call.

- [ ] **Step 1: Write the failing test**

Add to `internal/httpapi/selfupdate_test.go`:

```go
func TestSelfUpdateEndpointForceBypassesCache(t *testing.T) {
	var hits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.0.0","html_url":"https://x/y"}`))
	}))
	t.Cleanup(gh.Close)

	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, selfUpdateDeps(t, db, gh.URL, "0.4.2"))

	// First cached call warms the cache (1 hit).
	if rec := authedGet(t, srv, "/api/updates/self", tok, csrf); rec.Code != http.StatusOK {
		t.Fatalf("warm: want 200, got %d", rec.Code)
	}
	// Forced call must refetch despite the fresh cache (2nd hit).
	rec := authedGet(t, srv, "/api/updates/self?force=true", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("force: want 200, got %d", rec.Code)
	}
	if hits != 2 {
		t.Errorf("force=true must bypass cache; GitHub hits=%d, want 2", hits)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["latest"] != "v9.0.0" || out["update_available"] != true {
		t.Errorf("forced verdict: %v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestSelfUpdateEndpointForce -v`
Expected: FAIL — `hits=1, want 2` (the handler currently always serves the cached path).

- [ ] **Step 3: Read the force param in the handler**

In `internal/httpapi/selfupdate.go`, replace the body of `handleSelfUpdate` (keep the nil-checker short-circuit) so it selects the checker method:

```go
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfUpdate == nil {
		writeJSON(w, http.StatusOK, map[string]any{"update_available": false})
		return
	}
	// ?force=true (or 1) bypasses the cache TTL for the manual "Check for
	// updates" action; the default poll keeps serving the cached verdict.
	var res selfupdate.Result
	if force := r.URL.Query().Get("force"); force == "true" || force == "1" {
		res, _ = s.deps.SelfUpdate.CheckFresh(r.Context()) // soft error: res is still a valid verdict
	} else {
		res, _ = s.deps.SelfUpdate.Check(r.Context())
	}
	out := map[string]any{
		"current":          res.Current,
		"latest":           res.Latest,
		"html_url":         res.HTMLURL,
		"update_available": res.UpdateAvailable,
	}
	if !res.CheckedAt.IsZero() {
		out["checked_at"] = res.CheckedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}
```

Add the import `"dockbrr/internal/selfupdate"` to the file's import block (it is not currently imported in `selfupdate.go`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ -run TestSelfUpdate -v`
Expected: PASS — the force test and the existing `TestSelfUpdateEndpoint` / `TestSelfUpdateEndpointNilDep` all pass.

- [ ] **Step 5: Verify the build stays CGO-free and vet is clean**

Run: `CGO_ENABLED=0 go build ./... && go vet ./internal/httpapi/ ./internal/selfupdate/`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/selfupdate.go internal/httpapi/selfupdate_test.go
git commit -m "feat(api): support ?force=true on GET /api/updates/self"
```

---

### Task 3: Frontend button, status line, and forced-check hook

**Files:**
- Modify: `web/src/hooks/mutations.ts`
- Modify: `web/src/components/settings/ApplicationSettings.tsx`
- Test: `web/src/components/settings/ApplicationSettings.test.tsx`

**Interfaces:**
- Consumes: `GET /api/updates/self?force=true` (Task 2); existing `useSelfUpdate()` from `@/hooks/queries`; `keys.selfUpdate`; `SelfUpdate` type from `@/api/types`; `useNow` from `@/hooks/useNow`; `cn` from `@/lib/cn`.
- Produces: `useCheckForUpdates()` returning a mutation `{ mutate, isPending }` that writes its result into the `keys.selfUpdate` query cache.

- [ ] **Step 1: Write the failing component tests**

The current test stubs `fetch` to return `SystemInfo` for every URL. First, update `renderPage` so it routes by URL and can also serve a self-update payload, then add assertions.

Replace the `renderPage` helper in `ApplicationSettings.test.tsx` with:

```tsx
import type { SystemInfo, SelfUpdate } from "@/api/types";

const NO_UPDATE: SelfUpdate = { current: "0.1.0-dev", update_available: false, checked_at: "2026-07-12T12:00:00Z" };

function renderPage(info: SystemInfo, self: SelfUpdate = NO_UPDATE) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
      const body = url.includes("/api/updates/self") ? self : info;
      return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
    }),
  );
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <ApplicationSettings />
    </QueryClientProvider>,
  );
}
```

Then add these tests (the clock is frozen to `2026-07-12T12:00:00Z` region by the existing `beforeEach`; `checked_at` equal to the frozen now renders "just now"):

```tsx
it("shows 'up to date' status under the version", async () => {
  renderPage(FULL, { current: "0.1.0-dev", update_available: false, checked_at: "2026-07-12T12:00:00Z" });
  expect(await screen.findByText(/Up to date/i)).toBeInTheDocument();
});

it("shows an available version when an update exists", async () => {
  renderPage(FULL, { current: "0.1.0-dev", latest: "0.2.0", update_available: true, checked_at: "2026-07-12T12:00:00Z" });
  expect(await screen.findByText(/0\.2\.0 available/i)).toBeInTheDocument();
});

it("checks for updates with force=true on click", async () => {
  renderPage(FULL);
  const btn = await screen.findByRole("button", { name: /check for updates/i });
  fireEvent.click(btn);
  await waitFor(() =>
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      expect.stringContaining("/api/updates/self?force=true"),
      expect.anything(),
    ),
  );
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- ApplicationSettings`
Expected: FAIL — no "Up to date" text and no "check for updates" button in the current component.

- [ ] **Step 3: Add the `useCheckForUpdates` hook**

In `web/src/hooks/mutations.ts`, extend the type import and add the hook. Change the import line:

```tsx
import type { Scope, SelfUpdate } from "@/api/types";
```

Add near `useApplySelfUpdate`:

```tsx
// useCheckForUpdates forces a fresh GitHub check (bypassing the 6h cache TTL)
// and writes the verdict into the shared keys.selfUpdate cache, so the sidebar
// UpdateNotice and the Settings build card both reflect it immediately.
export function useCheckForUpdates() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<SelfUpdate>("/api/updates/self?force=true"),
    onSuccess: (data) => qc.setQueryData(keys.selfUpdate, data),
    onError: toastError,
  });
}
```

(`toastError` is already defined and used by `useApplySelfUpdate` in this file; reuse it.)

- [ ] **Step 4: Wire the button + status line into the Build card**

In `web/src/components/settings/ApplicationSettings.tsx`:

Add imports at the top:

```tsx
import { cn } from "@/lib/cn";
import { useSelfUpdate } from "@/hooks/queries";
import { useCheckForUpdates } from "@/hooks/mutations";
```

(`useSystemInfo`, `useNow`, `RefreshCw`, `Button`, `SettingsCard`, `InfoRow` are already imported.)

Add a relative-time helper next to `formatDate`:

```tsx
// checkedAgo renders "just now" / "5m ago" / "3h ago" / "2d ago" from an ISO
// timestamp against a ticking clock, so the last-checked line ages on screen.
function checkedAgo(iso: string | undefined, now: Date): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.floor((now.getTime() - t) / 1000));
  if (s < 60) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}
```

Inside the component, after the existing `const { data, isLoading, refetch, isFetching } = useSystemInfo();` line, add:

```tsx
  const selfUpdate = useSelfUpdate();
  const check = useCheckForUpdates();
```

After `const commit = ...`, derive the version sub-line:

```tsx
  const su = selfUpdate.data;
  const rel = checkedAgo(su?.checked_at, now);
  // Only assert a verdict once a check has actually run (checked_at present);
  // otherwise show nothing rather than claiming "up to date" with no basis.
  const versionSub = su?.checked_at
    ? su.update_available
      ? `${su.latest} available${rel ? ` (checked ${rel})` : ""}`
      : `Up to date${rel ? ` (checked ${rel})` : ""}`
    : undefined;
```

Replace the Build `SettingsCard` `action` prop with two buttons:

```tsx
        action={
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
              <RefreshCw className="mr-2 h-4 w-4" />
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => check.mutate()} disabled={check.isPending}>
              <RefreshCw className={cn("mr-2 h-4 w-4", check.isPending && "animate-spin")} />
              {check.isPending ? "Checking..." : "Check for updates"}
            </Button>
          </div>
        }
```

Give the Version `InfoRow` the sub-line:

```tsx
          <InfoRow label="Version" value={data.version} sub={versionSub} />
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd web && npm test -- ApplicationSettings`
Expected: PASS — all three new tests plus the pre-existing ApplicationSettings tests.

- [ ] **Step 6: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no type errors. (Do NOT rely on `npx tsc`; the rtk proxy masks its errors.)

- [ ] **Step 7: Commit**

```bash
git add web/src/hooks/mutations.ts web/src/components/settings/ApplicationSettings.tsx web/src/components/settings/ApplicationSettings.test.tsx
git commit -m "feat(web): add manual check-for-updates to Application settings"
```

---

### Task 4: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full check suite**

Run: `mise run check`
Expected: `go vet` clean, all Go tests pass, all vitest suites pass.

- [ ] **Step 2: Confirm the static-binary invariant**

Run: `CGO_ENABLED=0 go build ./...`
Expected: builds with no output.

- [ ] **Step 3: Manual smoke (optional, if a dev server is handy)**

Run `mise run dev`, open Settings > Application, click "Check for updates". Expect the button to show "Checking..." briefly, then a status line under Version. With a dev build version the verdict is "Up to date (checked just now)".

---

## Self-Review

**Spec coverage:**
- Force-refresh backend path → Task 1 (`CheckFresh`) + Task 2 (`?force=true`). ✓
- Same JSON body for forced/cached → Task 2 (shared `out` map). ✓
- `useSelfUpdate` unchanged, forced fetch writes shared cache → Task 3 (`useCheckForUpdates` + `setQueryData`). ✓
- Button in Build card action slot beside Refresh → Task 3 Step 4. ✓
- Version sub-line "up to date" / "vX available" + relative checked time → Task 3 (`versionSub` + `checkedAgo`). ✓
- Omit sub-line when no `checked_at` basis → Task 3 (`su?.checked_at` guard). ✓
- Pending state disables the button → Task 3 (`disabled={check.isPending}`). ✓
- Dev build / nil checker / GitHub error edge cases → covered by existing checker behavior + Task 1 stale/no-cache tests; no apply here so no 409 path involved. ✓
- Tests: checker force, handler force, component status + forced fetch → Tasks 1-3. ✓

**Placeholder scan:** none — every code step shows complete code.

**Type consistency:** `CheckFresh(ctx) (Result, error)` used identically in Tasks 1-2; `useCheckForUpdates()` returns `{ mutate, isPending }` used in Task 3; `SelfUpdate` fields (`latest`, `update_available`, `checked_at`, `current`) match `web/src/api/types.ts`. ✓
