# Self-Update Notification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a dismissable "Update Available" card in the sidebar when a newer stable dockbrr release exists on GitHub, with a "View Release" button linking to the release page.

**Architecture:** A new isolated `internal/selfupdate` package fetches the latest stable release from GitHub (`/releases/latest`), caches the result in the settings store with a TTL, and compares it to the build version via `internal/detect`. A new authed `GET /api/updates/self` endpoint exposes the result; a React `UpdateNotice` component renders the card above the Logout button and remembers per-version dismissal in localStorage.

**Tech Stack:** Go 1.26 (chi router, `internal/store` SQLite settings, `internal/detect` semver), React + TanStack Query + Tailwind v4 + shadcn/Radix, vitest + MSW.

## Global Constraints

- CGO-free: `CGO_ENABLED=0 go build ./...` must stay green (no new cgo deps).
- No CDN / self-contained frontend; icons from `lucide-react` only.
- External links: `target="_blank" rel="noreferrer"` (matches existing sidebar GitHub link).
- Repo is `yorah/dockbrr`; GitHub API base `https://api.github.com`.
- Brand token is `--primary` (blue); the update card uses the on-palette `--success` (green) accent to read as a positive notice. No new color tokens.
- TS typecheck via `./node_modules/.bin/tsc -b --noEmit` (NOT `npx tsc`); `npm run build` is the backstop.
- Verify commands: `go vet ./... && go test ./...` (backend); `cd web && npm test` (frontend).

---

### Task 1: `internal/selfupdate` Checker

**Files:**
- Create: `internal/selfupdate/checker.go`
- Test: `internal/selfupdate/checker_test.go`

**Interfaces:**
- Consumes: `detect.ParseCore(v string) ([3]int, bool)`, `detect.CoreLess(a, b [3]int) bool`, `*store.Settings` (`Get(key) (string, error)`, `Set(key, value) error`, `store.ErrSettingNotFound`).
- Produces:
  - `type Result struct { Current, Latest, HTMLURL string; UpdateAvailable bool; CheckedAt time.Time }`
  - `func NewChecker(httpClient *http.Client, settings *store.Settings, current, apiBase string, ttl time.Duration, tokenFn func() string) *Checker`
  - `func (c *Checker) Check(ctx context.Context) (Result, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/selfupdate/checker_test.go`:

```go
package selfupdate_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/secret"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

func newSettings(t *testing.T) *store.Settings {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secret.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return store.NewSettings(db, sealer)
}

// ghServer returns an httptest server that serves one releases/latest payload
// and counts hits, so tests can assert the cache path skips the network.
func ghServer(t *testing.T, tag, htmlURL string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path != "/repos/yorah/dockbrr/releases/latest" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"` + htmlURL + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckFreshFetchDetectsNewer(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://github.com/yorah/dockbrr/releases/tag/v0.5.0", &hits)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpdateAvailable {
		t.Errorf("want update available, got %+v", res)
	}
	if res.Latest != "v0.5.0" || res.HTMLURL == "" {
		t.Errorf("latest/url: %+v", res)
	}
	if hits != 1 {
		t.Errorf("want 1 GitHub hit, got %d", hits)
	}
}

func TestCheckCacheHitSkipsNetwork(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://x/y", &hits)
	s := newSettings(t)
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	if _, err := c.Check(context.Background()); err != nil { // warms cache
		t.Fatal(err)
	}
	if _, err := c.Check(context.Background()); err != nil { // served from cache
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("cache hit should not re-fetch; hits=%d", hits)
	}
}

func TestCheckStaleCacheRefetches(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://x/y", &hits)
	s := newSettings(t)
	// Seed a stale cache: checked_at far in the past, TTL short.
	_ = s.Set("selfupdate_latest_tag", "v0.4.9")
	_ = s.Set("selfupdate_latest_url", "https://old")
	_ = s.Set("selfupdate_checked_at", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Latest != "v0.5.0" || hits != 1 {
		t.Errorf("stale cache should refetch; res=%+v hits=%d", res, hits)
	}
}

func TestCheckGitHubErrorServesStaleCache(t *testing.T) {
	// Server always 500s; a stale cache is present.
	var hits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	s := newSettings(t)
	_ = s.Set("selfupdate_latest_tag", "v0.5.0")
	_ = s.Set("selfupdate_latest_url", "https://stale")
	_ = s.Set("selfupdate_checked_at", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if res.Latest != "v0.5.0" || !res.UpdateAvailable {
		t.Errorf("want stale v0.5.0 served, got %+v", res)
	}
}

func TestCheckGitHubErrorNoCache(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err == nil {
		t.Error("want soft error when no cache and GitHub fails")
	}
	if res.UpdateAvailable {
		t.Errorf("no cache + error must not claim an update: %+v", res)
	}
	if res.Current != "0.4.2" {
		t.Errorf("current should still be reported: %+v", res)
	}
}

func TestUpdateAvailableMatrix(t *testing.T) {
	cases := []struct {
		name, current, latest string
		want                  bool
	}{
		{"newer", "0.4.2", "v0.5.0", true},
		{"equal", "0.5.0", "v0.5.0", false},
		{"older", "0.5.1", "v0.5.0", false},
		{"dev-current-unparsable", "dev", "v0.5.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			gh := ghServer(t, tc.latest, "https://x/y", &hits)
			c := selfupdate.NewChecker(gh.Client(), newSettings(t), tc.current, gh.URL, time.Hour, nil)
			res, err := c.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.UpdateAvailable != tc.want {
				t.Errorf("%s: want %v, got %v", tc.name, tc.want, res.UpdateAvailable)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/...`
Expected: FAIL — `package dockbrr/internal/selfupdate is not in std` / undefined `selfupdate.NewChecker`.

- [ ] **Step 3: Write the implementation**

Create `internal/selfupdate/checker.go`:

```go
// Package selfupdate reports whether a newer stable dockbrr release is
// available on GitHub. It is read-only and best-effort: a GitHub outage or
// rate-limit never blocks or errors the UI, it degrades to "no update".
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"dockbrr/internal/detect"
	"dockbrr/internal/store"
)

const (
	repoPath     = "yorah/dockbrr"
	keyTag       = "selfupdate_latest_tag"
	keyURL       = "selfupdate_latest_url"
	keyCheckedAt = "selfupdate_checked_at"
)

// Result is a point-in-time answer to "is there a newer release?".
type Result struct {
	Current         string    // the running build version
	Latest          string    // latest stable tag from GitHub (as returned, e.g. "v0.5.0")
	HTMLURL         string    // release page URL
	UpdateAvailable bool      // Latest's numeric core is greater than Current's
	CheckedAt       time.Time // when Latest was last fetched from GitHub
}

// Checker resolves the latest release, caching it in the settings store so a
// busy dashboard does not hammer the GitHub API.
type Checker struct {
	http     *http.Client
	settings *store.Settings
	current  string
	ttl      time.Duration
	apiBase  string
	tokenFn  func() string
}

// NewChecker wires a Checker. apiBase defaults to https://api.github.com; a nil
// tokenFn is treated as "no token".
func NewChecker(httpClient *http.Client, settings *store.Settings, current, apiBase string, ttl time.Duration, tokenFn func() string) *Checker {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	if tokenFn == nil {
		tokenFn = func() string { return "" }
	}
	return &Checker{http: httpClient, settings: settings, current: current, ttl: ttl, apiBase: apiBase, tokenFn: tokenFn}
}

// Check returns the latest-release verdict. It serves a cached answer when the
// cache is younger than the TTL, otherwise it refetches. On a GitHub failure it
// falls back to a stale cache when one exists (returning nil error), and only
// returns an error when there is nothing cached to fall back on.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	tag, url, checkedAt, haveCache := c.readCache()
	if haveCache && time.Since(checkedAt) < c.ttl {
		return c.result(tag, url, checkedAt), nil
	}

	fTag, fURL, err := c.fetchLatest(ctx)
	if err != nil {
		if haveCache {
			// Best-effort: serve stale and leave checked_at untouched so the
			// next request retries GitHub rather than waiting out the TTL.
			return c.result(tag, url, checkedAt), nil
		}
		return Result{Current: c.current}, err
	}

	now := time.Now().UTC()
	c.writeCache(fTag, fURL, now)
	return c.result(fTag, fURL, now), nil
}

func (c *Checker) result(tag, url string, checkedAt time.Time) Result {
	return Result{
		Current:         c.current,
		Latest:          tag,
		HTMLURL:         url,
		UpdateAvailable: isNewer(c.current, tag),
		CheckedAt:       checkedAt,
	}
}

// isNewer reports whether latest's numeric core exceeds current's. An
// unparsable current (dev build) yields false, so dev builds are never nagged.
func isNewer(current, latest string) bool {
	cur, ok1 := detect.ParseCore(current)
	lat, ok2 := detect.ParseCore(latest)
	if !ok1 || !ok2 {
		return false
	}
	return detect.CoreLess(cur, lat)
}

func (c *Checker) fetchLatest(ctx context.Context) (tag, htmlURL string, err error) {
	// /releases/latest excludes drafts and pre-releases: stable only.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/repos/"+repoPath+"/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := c.tokenFn(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github releases/latest: status %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	if body.TagName == "" {
		return "", "", errors.New("github releases/latest: empty tag_name")
	}
	return body.TagName, body.HTMLURL, nil
}

func (c *Checker) readCache() (tag, url string, checkedAt time.Time, ok bool) {
	tag, err := c.settings.Get(keyTag)
	if err != nil || tag == "" {
		return "", "", time.Time{}, false
	}
	url, _ = c.settings.Get(keyURL)
	ts, err := c.settings.Get(keyCheckedAt)
	if err != nil {
		return "", "", time.Time{}, false
	}
	checkedAt, err = time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", "", time.Time{}, false
	}
	return tag, url, checkedAt, true
}

func (c *Checker) writeCache(tag, url string, at time.Time) {
	// Best-effort persistence: a failed write just means the next request refetches.
	_ = c.settings.Set(keyTag, tag)
	_ = c.settings.Set(keyURL, url)
	_ = c.settings.Set(keyCheckedAt, at.Format(time.RFC3339))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/...`
Expected: PASS (all tests).

- [ ] **Step 5: Vet + commit**

```bash
go vet ./internal/selfupdate/...
git add internal/selfupdate/
git commit -m "feat(selfupdate): checker for latest dockbrr release with TTL cache"
```

---

### Task 2: `GET /api/updates/self` endpoint + wiring

**Files:**
- Create: `internal/httpapi/selfupdate.go`
- Modify: `internal/httpapi/server.go` (add `SelfUpdate` to `Deps`, register route)
- Modify: `cmd/dockbrr/main.go` (construct checker, add to `Deps`, startup warm)
- Test: `internal/httpapi/selfupdate_test.go`

**Interfaces:**
- Consumes: `selfupdate.NewChecker`, `(*selfupdate.Checker).Check`, `Result` (Task 1); existing `writeJSON`, `authedServer`, `mergeDeps`, `authedGet` test helpers; existing `tokenFn` and `httpClient` in `main.go`.
- Produces: JSON body `{ current, latest, html_url, update_available, checked_at }` at `GET /api/updates/self`.

- [ ] **Step 1: Write the failing handler test**

Create `internal/httpapi/selfupdate_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dockbrr/internal/secret"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

func selfUpdateDeps(t *testing.T, db *store.DB, apiBase, current string) Deps {
	t.Helper()
	key, _ := secret.LoadOrCreateKey(t.TempDir())
	sealer, _ := secret.NewSealer(key)
	settings := store.NewSettings(db, sealer)
	return Deps{
		Sealer:     sealer,
		Settings:   settings,
		SelfUpdate: selfupdate.NewChecker(http.DefaultClient, settings, current, apiBase, time.Hour, nil),
	}
}

func TestSelfUpdateEndpoint(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.0.0","html_url":"https://github.com/yorah/dockbrr/releases/tag/v9.0.0"}`))
	}))
	t.Cleanup(gh.Close)

	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, selfUpdateDeps(t, db, gh.URL, "0.4.2"))

	rec := authedGet(t, srv, "/api/updates/self", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["update_available"] != true {
		t.Errorf("update_available: %v", out)
	}
	if out["latest"] != "v9.0.0" {
		t.Errorf("latest: %v", out)
	}
	if out["html_url"] != "https://github.com/yorah/dockbrr/releases/tag/v9.0.0" {
		t.Errorf("html_url: %v", out)
	}
}

func TestSelfUpdateEndpointNilDep(t *testing.T) {
	// Deps without a SelfUpdate checker must degrade to update_available:false,
	// never panic (mirrors the nil-dep tolerance of other handlers).
	srv, _, tok, csrf := authedServer(t, Deps{})
	rec := authedGet(t, srv, "/api/updates/self", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["update_available"] != false {
		t.Errorf("nil dep should be false: %v", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/ -run TestSelfUpdate`
Expected: FAIL — `Deps has no field SelfUpdate` / route 404.

- [ ] **Step 3: Add the handler**

Create `internal/httpapi/selfupdate.go`:

```go
package httpapi

import (
	"net/http"
	"time"
)

// handleSelfUpdate reports whether a newer dockbrr release is available. It is
// best-effort: a nil checker or a swallowed GitHub error yields a valid body
// with update_available:false, never a 5xx.
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfUpdate == nil {
		writeJSON(w, http.StatusOK, map[string]any{"update_available": false})
		return
	}
	res, _ := s.deps.SelfUpdate.Check(r.Context()) // soft error: res is still a valid verdict
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

- [ ] **Step 4: Wire `Deps` + route**

In `internal/httpapi/server.go`, add the import `"dockbrr/internal/selfupdate"` to the import block, then add a field to the `Deps` struct (next to `DockerVersion`):

```go
	// SelfUpdate reports whether a newer dockbrr release is available. Optional:
	// nil (as in tests) degrades /api/updates/self to update_available:false.
	SelfUpdate *selfupdate.Checker
```

In `routes()`, inside the authed `s.mux.Group`, register the route next to `r.Get("/api/status", s.handleStatus)`:

```go
		r.Get("/api/updates/self", s.handleSelfUpdate)
```

- [ ] **Step 5: Run handler tests to verify they pass**

Run: `go test ./internal/httpapi/ -run TestSelfUpdate`
Expected: PASS (both tests).

- [ ] **Step 6: Construct the checker in `main.go` + startup warm**

In `cmd/dockbrr/main.go`, add `"dockbrr/internal/selfupdate"` to the imports. Immediately after the existing `tokenFn := func() string { ... }` block (~line 176, after it is defined), construct the checker:

```go
	// Self-update check: latest stable dockbrr release from GitHub, cached 6h.
	// Reuses tokenFn (the changelog GitHub token) to lift the anonymous rate limit.
	selfUpdateChecker := selfupdate.NewChecker(httpClient, settings, version.Version, "https://api.github.com", 6*time.Hour, tokenFn)
```

Add it to the `httpapi.Deps` literal (~line 316), alongside `Settings`/`Engine`:

```go
		SelfUpdate: selfUpdateChecker,
```

Warm the cache at boot so the first sidebar render is not a cold GitHub round-trip. Add this next to the `go func() { ... ListenAndServe ... }()` block (after `srv := httpapi.New(...)`), tied to the signal ctx:

```go
	// Warm the self-update cache off the request path (best-effort).
	go func() { _, _ = selfUpdateChecker.Check(ctx) }()
```

(`version` is already imported in `main.go`; confirm it is present.)

- [ ] **Step 7: Full backend verify**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/httpapi/selfupdate.go internal/httpapi/selfupdate_test.go internal/httpapi/server.go cmd/dockbrr/main.go
git commit -m "feat(httpapi): expose GET /api/updates/self with startup cache warm"
```

---

### Task 3: Frontend data layer

**Files:**
- Modify: `web/src/api/types.ts` (add `SelfUpdate`)
- Modify: `web/src/api/keys.ts` (add `selfUpdate` key)
- Modify: `web/src/hooks/queries.ts` (add `useSelfUpdate`)
- Modify: `web/src/test/msw.ts` (default handler so app-tree tests don't 404)

**Interfaces:**
- Consumes: `apiFetch`, `useQuery`, `keys` (existing).
- Produces: `useSelfUpdate()` → `{ data?: SelfUpdate }`; `SelfUpdate` type; `keys.selfUpdate`.

- [ ] **Step 1: Add the type**

In `web/src/api/types.ts`, after the `SystemStatus` interface, add:

```ts
export interface SelfUpdate {
  current: string;
  latest: string;
  html_url: string;
  update_available: boolean;
  checked_at?: string;
}
```

- [ ] **Step 2: Add the query key**

In `web/src/api/keys.ts`, add inside the `keys` object (near `lastApplied`, keeping the `["updates"]` prefix so update invalidations also refresh it):

```ts
  selfUpdate: ["updates", "self"] as const,
```

- [ ] **Step 3: Add the hook**

In `web/src/hooks/queries.ts`, add `SelfUpdate` to the type import from `@/api/types`, then add the hook next to `useStatus`:

```ts
export const useSelfUpdate = () =>
  useQuery({
    queryKey: keys.selfUpdate,
    queryFn: () => apiFetch<SelfUpdate>("/api/updates/self"),
    refetchInterval: 6 * 60 * 60 * 1000,
  });
```

- [ ] **Step 4: Add the default MSW handler**

In `web/src/test/msw.ts`, add to the `handlers` array (so every app-tree test that mounts the sidebar has a default):

```ts
  http.get("/api/updates/self", () =>
    HttpResponse.json({
      current: "0.0.0-test",
      latest: "0.0.0-test",
      html_url: "https://github.com/yorah/dockbrr/releases/latest",
      update_available: false,
    })),
```

- [ ] **Step 5: Typecheck + commit**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no errors.

```bash
git add web/src/api/types.ts web/src/api/keys.ts web/src/hooks/queries.ts web/src/test/msw.ts
git commit -m "feat(web): add useSelfUpdate query + type for /api/updates/self"
```

---

### Task 4: `UpdateNotice` component

**Files:**
- Create: `web/src/components/layout/UpdateNotice.tsx`
- Test: `web/src/components/layout/UpdateNotice.test.tsx`

**Interfaces:**
- Consumes: `useSelfUpdate` (Task 3), `buttonVariants` from `@/components/ui/button`, `Tooltip*` from `@/components/ui/tooltip`, `cn` from `@/lib/cn`, `Download`/`X` from `lucide-react`, `renderWithClient` + MSW from tests.
- Produces: `export function UpdateNotice({ collapsed }: { collapsed: boolean })`. Exports `DISMISS_KEY` for the test.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/layout/UpdateNotice.test.tsx`:

```tsx
import { beforeEach, describe, expect, test } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { UpdateNotice, DISMISS_KEY } from "./UpdateNotice";

const available = {
  current: "0.4.2",
  latest: "v0.5.0",
  html_url: "https://github.com/yorah/dockbrr/releases/tag/v0.5.0",
  update_available: true,
};

beforeEach(() => {
  localStorage.clear();
});

describe("UpdateNotice", () => {
  test("renders the card and a View Release link when an update is available", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);

    expect(await screen.findByText(/update available/i)).toBeInTheDocument();
    expect(screen.getByText(/v0\.5\.0 is now available/i)).toBeInTheDocument();
    const link = screen.getByRole("link", { name: /view release/i });
    expect(link).toHaveAttribute("href", available.html_url);
    expect(link).toHaveAttribute("target", "_blank");
  });

  test("renders nothing when no update is available", async () => {
    server.use(http.get("/api/updates/self", () =>
      HttpResponse.json({ ...available, update_available: false })));
    const { container } = renderWithClient(<UpdateNotice collapsed={false} />);
    // Give the query a tick; the card must never appear.
    await waitFor(() => expect(screen.queryByText(/update available/i)).not.toBeInTheDocument());
    expect(container).toBeEmptyDOMElement();
  });

  test("dismiss hides the card and records the tag in localStorage", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);

    const dismiss = await screen.findByRole("button", { name: /dismiss/i });
    await userEvent.click(dismiss);

    expect(localStorage.getItem(DISMISS_KEY)).toBe("v0.5.0");
    expect(screen.queryByText(/update available/i)).not.toBeInTheDocument();
  });

  test("stays hidden when the latest tag was already dismissed", async () => {
    localStorage.setItem(DISMISS_KEY, "v0.5.0");
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);
    await waitFor(() => expect(screen.queryByText(/update available/i)).not.toBeInTheDocument());
  });

  test("reappears when a newer tag ships after an old dismissal", async () => {
    localStorage.setItem(DISMISS_KEY, "v0.4.9"); // dismissed an older release
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);
    expect(await screen.findByText(/update available/i)).toBeInTheDocument();
  });

  test("collapsed variant renders an icon-only link, no card text", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={true} />);
    const link = await screen.findByRole("link", { name: /update available/i });
    expect(link).toHaveAttribute("href", available.html_url);
    expect(screen.queryByText(/view release/i)).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- UpdateNotice`
Expected: FAIL — cannot resolve `./UpdateNotice`.

- [ ] **Step 3: Write the component**

Create `web/src/components/layout/UpdateNotice.tsx`:

```tsx
import { useState } from "react";
import { Download, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { buttonVariants } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useSelfUpdate } from "@/hooks/queries";

export const DISMISS_KEY = "dockbrr_dismissed_update";

/**
 * UpdateNotice shows a dismissable "Update Available" card in the sidebar when a
 * newer stable dockbrr release exists. Dismissal is per-version: hiding v0.5.0
 * keeps it hidden until a newer tag ships. In the collapsed sidebar it shrinks
 * to an icon-only link, mirroring the collapsed Logout button.
 */
export function UpdateNotice({ collapsed }: { collapsed: boolean }) {
  const { data } = useSelfUpdate();
  const [dismissed, setDismissed] = useState<string | null>(() => localStorage.getItem(DISMISS_KEY));

  if (!data?.update_available) return null;
  if (dismissed === data.latest) return null;

  const dismiss = () => {
    localStorage.setItem(DISMISS_KEY, data.latest);
    setDismissed(data.latest);
  };

  if (collapsed) {
    return (
      <div className="px-2">
        <Tooltip>
          <TooltipTrigger asChild>
            <a
              href={data.html_url}
              target="_blank"
              rel="noreferrer"
              aria-label={`Update available: ${data.latest}`}
              className="flex h-9 items-center justify-center rounded-md text-success transition-colors hover:bg-accent"
            >
              <Download className="h-4 w-4 shrink-0" />
            </a>
          </TooltipTrigger>
          <TooltipContent side="right">Update available: {data.latest}</TooltipContent>
        </Tooltip>
      </div>
    );
  }

  return (
    <div className="px-2">
      <div className="rounded-md border border-success/40 bg-success/10 p-3">
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2 text-sm font-medium text-success">
            <Download className="h-4 w-4 shrink-0" />
            <span>Update Available</span>
          </div>
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss update notification"
            className="text-muted-foreground transition-colors hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          Version {data.latest} is now available
        </p>
        <a
          href={data.html_url}
          target="_blank"
          rel="noreferrer"
          className={cn(buttonVariants({ variant: "outline", size: "sm" }), "mt-2")}
        >
          View Release
        </a>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- UpdateNotice`
Expected: PASS (all six tests).

- [ ] **Step 5: Typecheck + commit**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no errors.

```bash
git add web/src/components/layout/UpdateNotice.tsx web/src/components/layout/UpdateNotice.test.tsx
git commit -m "feat(web): UpdateNotice sidebar card with per-version dismissal"
```

---

### Task 5: Mount `UpdateNotice` in the Sidebar

**Files:**
- Modify: `web/src/components/layout/Sidebar.tsx`
- Modify: `web/src/components/layout/Sidebar.test.tsx` (add a render assertion)

**Interfaces:**
- Consumes: `UpdateNotice` (Task 4), the sidebar's existing `collapsed` prop.
- Produces: card rendered directly above the Logout button.

- [ ] **Step 1: Write the failing test**

In `web/src/components/layout/Sidebar.test.tsx`, add a test inside the `describe("Sidebar", ...)` block:

```tsx
  test("shows the update notice above logout when an update is available", async () => {
    localStorage.clear();
    server.use(http.get("/api/updates/self", () => HttpResponse.json({
      current: "0.4.2", latest: "v0.5.0",
      html_url: "https://github.com/yorah/dockbrr/releases/tag/v0.5.0",
      update_available: true,
    })));
    renderApp("/");

    expect(await screen.findByText(/update available/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /view release/i })).toHaveAttribute(
      "href",
      "https://github.com/yorah/dockbrr/releases/tag/v0.5.0",
    );
  });
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- Sidebar`
Expected: FAIL — "Update Available" text not found (component not mounted yet).

- [ ] **Step 3: Mount the component**

In `web/src/components/layout/Sidebar.tsx`:

Add the import near the other layout imports:

```tsx
import { UpdateNotice } from "@/components/layout/UpdateNotice";
```

Inside the `<div className="mt-auto flex flex-col gap-2">` block, render `<UpdateNotice />` immediately before the `<Separator />` that precedes the logout button:

```tsx
      <div className="mt-auto flex flex-col gap-2">
        <UpdateNotice collapsed={collapsed} />
        <Separator />
        <div className="px-2">
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm test -- Sidebar`
Expected: PASS. Confirm the existing "renders nav, projects, logout and the version" test still passes (the default MSW handler from Task 3 keeps the notice hidden there).

- [ ] **Step 5: Full frontend verify + commit**

Run: `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: all tests PASS, no type errors, build succeeds.

```bash
git add web/src/components/layout/Sidebar.tsx web/src/components/layout/Sidebar.test.tsx
git commit -m "feat(web): mount UpdateNotice above logout in sidebar"
```

---

## Final verification

- [ ] `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...` — green.
- [ ] `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build` — green.
- [ ] `mise run build` — SPA + static binary builds (restores dist placeholder).
- [ ] Manual smoke (optional, `mise run dev`): with a stubbed newer tag, the card shows above Logout; "View Release" opens the GitHub release; ✕ dismisses and it stays hidden on reload; collapsed sidebar shows the icon-only variant.

## Notes on scope / YAGNI

- No settings toggle to disable the check (single best-effort GET; never blocks or errors the UI).
- No pre-release notifications (`/releases/latest` is stable-only by definition).
- No in-app auto-update; the button links out to GitHub.
