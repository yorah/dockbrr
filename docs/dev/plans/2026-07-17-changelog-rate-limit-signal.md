# Changelog rate-limit signal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a GitHub-hosted image's changelog is empty because the GitHub Releases API throttled the resolve, record that per-update and tell the user in the UI to set a token; document the token in the README and the token tooltip.

**Architecture:** The GitHub changelog source flags a real 403/429 rate-limit as a typed error. The resolver reports it (only when the whole source chain finds no content). Scan persists a `changelog_status='rate_limited'` marker on the update. The API exposes the field and the frontend renders a rate-limit hint with a link to the token settings.

**Tech Stack:** Go 1.26 (CGO-free), SQLite, net/http; React + TypeScript + Vite + Tailwind + TanStack; vitest.

Spec: `docs/dev/specs/2026-07-17-changelog-rate-limit-signal-design.md`.

## Global Constraints

- Static binary invariant: `CGO_ENABLED=0`, no new cgo deps.
- Compose/Docker not touched here (read-only enrichment path only).
- Frontend: no CDN, no `dangerouslySetInnerHTML`; changelog rendered via react-markdown + rehype-sanitize (unchanged here).
- No em-dashes in prose or docs.
- Go tests: `CGO_ENABLED=0 go test ./...`. TS typecheck via `cd web && npm run build` (NOT `npx tsc`, the rtk hook masks tsc errors). Web tests via `cd web && npm test`.
- Column value vocabulary for `changelog_status`: `''` (resolved or genuinely absent) or `'rate_limited'`.

## Task Summary

| # | Task | Deps | Model | Reviewer | Plan section |
|---|------|------|-------|----------|--------------|
| 1 | changelog: `ErrRateLimited` + 403/429 detection | - | sonnet | sonnet | Task 1 |
| 2 | resolver: report rate-limit on full-chain miss | 1 | sonnet | sonnet | Task 2 |
| 3 | store: migration + column + `SetChangelogStatus` | - | sonnet | sonnet | Task 3 |
| 4 | scan: persist `rate_limited` / clear on success | 2,3 | sonnet | sonnet | Task 4 |
| 5 | httpapi: `changelog_status` on `updateDTO` | 3 | sonnet | sonnet | Task 5 |
| 6 | frontend: type + Changelog hint + drawers | 5 | sonnet | sonnet | Task 6 |
| 7 | docs: token tooltip + README section | - | sonnet | sonnet | Task 7 |

Whole-branch final review: opus (repo rule).

---

### Task 1: changelog `ErrRateLimited` + 403/429 detection

**Files:**
- Modify: `internal/changelog/github.go` (add `errors` import; add sentinel; extend `fetchReleasesPage` default case)
- Test: `internal/changelog/github_test.go` (add `errors` import; two tests)

**Interfaces:**
- Produces: `var changelog.ErrRateLimited error` (a sentinel other packages match with `errors.Is`). `GitHubSource.Resolve` returns it (wrapped or bare) when a releases request returns 403/429 with `X-RateLimit-Remaining: 0`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/changelog/github_test.go` (add `"errors"` to its import block):

```go
func TestGitHubRateLimitedYieldsErrRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	_, err := s.Resolve(context.Background(), ghInput("ghcr.io/autobrr/autobrr:latest", "1.82.1"))
	if !errors.Is(err, changelog.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestGitHubForbiddenWithoutRateHeaderIsGenericError(t *testing.T) {
	// A 403 that is NOT a primary-limit exhaustion (no X-RateLimit-Remaining:0),
	// and a 401 auth failure, must stay generic errors, not ErrRateLimited.
	for _, tc := range []struct {
		name   string
		status int
		remain string
	}{
		{"secondary-403", http.StatusForbidden, "42"},
		{"auth-401", http.StatusUnauthorized, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.remain != "" {
					w.Header().Set("X-RateLimit-Remaining", tc.remain)
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)
			s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
			_, err := s.Resolve(context.Background(), ghInput("ghcr.io/autobrr/autobrr:latest", "1.82.1"))
			if err == nil {
				t.Fatal("err = nil, want a non-nil generic error")
			}
			if errors.Is(err, changelog.ErrRateLimited) {
				t.Fatalf("err = %v, want NOT ErrRateLimited", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/changelog/ -run 'RateLimited|ForbiddenWithoutRateHeader' -v`
Expected: FAIL (compile error: `undefined: changelog.ErrRateLimited`).

- [ ] **Step 3: Add the sentinel and detection**

In `internal/changelog/github.go`, add `"errors"` to the import block. Above `type repoCache interface`, add:

```go
// ErrRateLimited signals that a GitHub Releases request was rejected for primary
// rate-limit exhaustion (HTTP 403/429 with X-RateLimit-Remaining: 0). It is
// distinct from an auth failure (401) or a forbidden resource, which stay
// generic errors. The resolver surfaces it only when the whole source chain
// finds no changelog content.
var ErrRateLimited = errors.New("changelog: github rate limited")
```

Replace the `default` case of the status switch in `fetchReleasesPage`:

```go
	default:
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, false, ErrRateLimited
		}
		return nil, false, fmt.Errorf("github releases: status %d", resp.StatusCode)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/changelog/ -v`
Expected: PASS (all existing changelog tests plus the two new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/changelog/github.go internal/changelog/github_test.go
git commit -m "feat(changelog): flag github rate-limit 403/429 as ErrRateLimited"
```

---

### Task 2: resolver reports rate-limit on a full-chain miss

**Files:**
- Modify: `internal/changelog/resolver.go` (add `errors` import; track `sawRateLimit`)
- Test: `internal/changelog/resolver_test.go`

**Interfaces:**
- Consumes: `changelog.ErrRateLimited` (Task 1).
- Produces: `(*Resolver).Resolve` returns `("", "", ErrRateLimited)` when every source misses AND at least one source failed with `ErrRateLimited`; otherwise unchanged (first non-empty content wins with a nil error; a plain miss returns `("", "", nil)`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/changelog/resolver_test.go`:

```go
func TestResolverReportsRateLimitWhenChainEmpty(t *testing.T) {
	r := changelog.NewResolver([]changelog.Source{
		fakeSource{name: "gh", err: changelog.ErrRateLimited},
		fakeSource{name: "oci", res: changelog.Result{}},
	})
	text, url, err := r.Resolve(context.Background(), store.Update{}, registry.RemoteImage{})
	if text != "" || url != "" {
		t.Fatalf("content = (%q,%q), want empty", text, url)
	}
	if !errors.Is(err, changelog.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestResolverIgnoresRateLimitWhenLaterSourceHasContent(t *testing.T) {
	// A rate-limited GitHub source followed by an OCI-label link: the link is
	// real content, so no rate-limit signal is emitted.
	r := changelog.NewResolver([]changelog.Source{
		fakeSource{name: "gh", err: changelog.ErrRateLimited},
		fakeSource{name: "oci", res: changelog.Result{URL: "https://github.com/acme/web"}},
	})
	_, url, err := r.Resolve(context.Background(), store.Update{}, registry.RemoteImage{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if url != "https://github.com/acme/web" {
		t.Fatalf("url = %q, want the oci link", url)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/changelog/ -run 'ResolverReportsRateLimit|ResolverIgnoresRateLimit' -v`
Expected: FAIL (`TestResolverReportsRateLimitWhenChainEmpty` gets a nil err).

- [ ] **Step 3: Track the flag in the loop**

In `internal/changelog/resolver.go`, add `"errors"` to the import block. Replace the source loop and trailing return in `Resolve`:

```go
	sawRateLimit := false
	for _, s := range r.sources {
		res, rerr := s.Resolve(ctx, in)
		if rerr != nil {
			if errors.Is(rerr, ErrRateLimited) {
				sawRateLimit = true
			}
			logger.Errorf("changelog: source %s: %v", s.Name(), rerr)
			continue
		}
		t := sanitizeText(res.Text)
		l := sanitizeURL(res.URL)
		if t != "" || l != "" {
			return t, l, nil
		}
	}
	if sawRateLimit {
		return "", "", ErrRateLimited
	}
	return "", "", nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/changelog/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/changelog/resolver.go internal/changelog/resolver_test.go
git commit -m "feat(changelog): resolver reports ErrRateLimited on a full-chain miss"
```

---

### Task 3: store migration + `changelog_status` column + `SetChangelogStatus`

**Files:**
- Create: `internal/store/migrations/0010_updates_changelog_status.sql`
- Modify: `internal/store/updates.go` (struct field; 5 SELECT/scan sites; `SetChangelog`; new `SetChangelogStatus`)
- Test: `internal/store/updates_test.go`

**Interfaces:**
- Produces: `store.Update.ChangelogStatus string`; `(*store.Updates).SetChangelogStatus(updateID int64, status string) error`; `SetChangelog` now also resets `changelog_status` to `''`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/store/updates_test.go`:

```go
func TestUpdatesSetChangelogStatus(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelogStatus(id, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	open, err := u.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("ChangelogStatus = %q (rows=%d), want rate_limited", func() string {
			if len(open) == 0 {
				return ""
			}
			return open[0].ChangelogStatus
		}(), len(open))
	}
}

func TestUpdatesSetChangelogClearsStatus(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelogStatus(id, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelog(id, "https://example.com/notes", "notes body"); err != nil {
		t.Fatal(err)
	}
	got, err := u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChangelogStatus != "" {
		t.Fatalf("ChangelogStatus = %q, want cleared", got.ChangelogStatus)
	}
	if got.ChangelogText != "notes body" || got.ChangelogURL != "https://example.com/notes" {
		t.Fatalf("changelog content = (%q,%q)", got.ChangelogURL, got.ChangelogText)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run 'SetChangelogStatus|SetChangelogClearsStatus' -v`
Expected: FAIL (compile error: `up.ChangelogStatus` and `SetChangelogStatus` undefined).

- [ ] **Step 3a: Add the migration**

Create `internal/store/migrations/0010_updates_changelog_status.sql`:

```sql
-- Records why an update's changelog is empty. '' means resolved normally or
-- genuinely absent; 'rate_limited' means the GitHub Releases API throttled the
-- resolve attempt (set a GitHub token to raise the limit).
ALTER TABLE updates ADD COLUMN changelog_status TEXT NOT NULL DEFAULT '';
```

- [ ] **Step 3b: Add the struct field**

In `internal/store/updates.go`, add to `type Update struct` right after `ChangelogText string`:

```go
	ChangelogStatus string // "" (resolved/absent) | "rate_limited"
```

- [ ] **Step 3c: Thread the column through all 5 SELECT/scan sites**

In each of `ListOpen`, `ListVisible`, `GetLatestOpenByService`, `ListLastAppliedByService`, `Get`: in the SELECT column list, change `changelog_url, changelog_text, status` to `changelog_url, changelog_text, changelog_status, status`; in the matching `Scan(...)`, change `&up.ChangelogText, &up.Status` to `&up.ChangelogText, &up.ChangelogStatus, &up.Status`.

Example (ListOpen SELECT + Scan, apply the same shape to the other four):

```go
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates WHERE status='available' ORDER BY detected_at DESC, id DESC`,
```

```go
		if err := rows.Scan(
			&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
			&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL,
			&up.ChangelogText, &up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
		); err != nil {
```

Note: `GetLatestOpenByService` and `Get` use `.Scan(...)` on a single `QueryRow`; add `&up.ChangelogStatus` between `&up.ChangelogText` and `&up.Status` there too. Leave `Upsert`'s INSERT column list unchanged: the new column has a `DEFAULT ''`, and the changelog columns are written only via `SetChangelog`/`SetChangelogStatus` (existing comment at the Upsert), so a fresh insert correctly starts at `''`.

- [ ] **Step 3d: Update `SetChangelog` and add `SetChangelogStatus`**

Replace `SetChangelog`'s statement so a successful resolve clears any prior marker:

```go
// SetChangelog persists the resolved changelog url + text on the update row and
// clears changelog_status (a successful resolve supersedes a prior rate-limit).
func (u *Updates) SetChangelog(updateID int64, url, text string) error {
	_, err := u.db.Exec(
		`UPDATE updates SET changelog_url=?, changelog_text=?, changelog_status='' WHERE id=?`,
		url, text, updateID,
	)
	return err
}
```

Add directly below it:

```go
// SetChangelogStatus records a non-content changelog outcome ("rate_limited")
// on the update row, leaving changelog_url/text untouched. Used when the
// resolve chain produced no content because GitHub throttled it.
func (u *Updates) SetChangelogStatus(updateID int64, status string) error {
	_, err := u.db.Exec(
		`UPDATE updates SET changelog_status=? WHERE id=?`,
		status, updateID,
	)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: PASS (new tests plus all existing store tests, which now scan the extra column).

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/0010_updates_changelog_status.sql internal/store/updates.go internal/store/updates_test.go
git commit -m "feat(store): add updates.changelog_status column + SetChangelogStatus"
```

---

### Task 4: scan persists `rate_limited` and clears it on a later success

**Files:**
- Modify: `internal/scan/scan.go` (add `errors` + `changelog` imports; replace the resolve-then-store block near line 124)
- Test: `internal/scan/scan_test.go` (add `err` field to `fakeChangelog`; add `changelog` import; new test)

**Interfaces:**
- Consumes: `changelog.ErrRateLimited` (Task 1/2); `(*store.Updates).SetChangelogStatus` (Task 3). The `Changelog` interface signature `Resolve(...) (text, url string, err error)` is unchanged.

- [ ] **Step 1: Write the failing test**

In `internal/scan/scan_test.go`, add `"dockbrr/internal/changelog"` to the import block, and add an `err` field to `fakeChangelog`:

```go
type fakeChangelog struct {
	text, url string
	err       error
	gotLabels map[string]string
}

func (f *fakeChangelog) Resolve(_ context.Context, _ store.Update, img registry.RemoteImage) (string, string, error) {
	f.gotLabels = img.Labels
	return f.text, f.url, f.err
}
```

Add the test:

```go
func TestCheckServicePersistsRateLimitedStatus(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3", CurrentDigest: "sha256:old",
	})
	updates := store.NewUpdates(db)
	uid, _ := updates.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})

	det := fakeDetector{upd: &store.Update{ID: uid, ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	cl := &fakeChangelog{err: changelog.ErrRateLimited}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	got, err := updates.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChangelogStatus != "rate_limited" {
		t.Fatalf("ChangelogStatus = %q, want rate_limited", got.ChangelogStatus)
	}
	if got.ChangelogText != "" || got.ChangelogURL != "" {
		t.Fatalf("changelog content = (%q,%q), want empty", got.ChangelogURL, got.ChangelogText)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -run 'PersistsRateLimitedStatus' -v`
Expected: FAIL (`ChangelogStatus = ""` and/or compile error until the scan branch is added).

- [ ] **Step 3: Replace the resolve-then-store block**

In `internal/scan/scan.go`, add `"errors"` and `"dockbrr/internal/changelog"` to the import block. Replace lines 124-133 (the `text, url, err := s.changelog.Resolve(...)` block through its `if text != "" || url != ""` store) with:

```go
	text, url, err := s.changelog.Resolve(ctx, *upd, remote)
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(upd.ID, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (update %d): %v", upd.ID, serr)
		}
	case err != nil:
		logger.Errorf("scan: changelog resolve (service %d (%s)): %v", serviceID, svc.Name, err)
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(upd.ID, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (update %d): %v", upd.ID, serr)
		}
	}
	return nil
```

(A genuine miss falls through all three cases: nothing is written and `changelog_status` stays `''`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -v`
Expected: PASS (new test plus the existing persist/no-update tests).

- [ ] **Step 5: Commit**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go
git commit -m "feat(scan): persist rate_limited changelog status, clear on success"
```

---

### Task 5: httpapi exposes `changelog_status`

**Files:**
- Modify: `internal/httpapi/updates.go` (`updateDTO` field + both handler mappings)
- Test: `internal/httpapi/updates_test.go` (assert the field serializes)

**Interfaces:**
- Consumes: `store.Update.ChangelogStatus` (Task 3).
- Produces: JSON `changelog_status` on every element of `/api/updates` and `/api/updates/last-applied`.

- [ ] **Step 1: Write the failing test**

Extend `internal/httpapi/updates_test.go` (it already has an `authedServer` + `authedGet` harness). Add a test that seeds an update, marks it rate-limited via `SetChangelogStatus`, and asserts `/api/updates` carries the field:

```go
func TestListUpdatesCarriesChangelogStatus(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	svcID := seedProjectAndService(t, srv)

	id, err := srv.deps.Updates.Upsert(store.Update{
		ServiceID: svcID, FromDigest: "sha256:aaa", ToDigest: "sha256:bbb",
		Tag: "1.3.0", Severity: "minor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.deps.Updates.SetChangelogStatus(id, "rate_limited"); err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, "/api/updates", tok, csrf)
	var got []updateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("dto = %+v, want changelog_status=rate_limited", got)
	}
}
```

(No new imports: `encoding/json`, `store`, `testing` are already imported.)

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run 'ChangelogStatus' -v`
Expected: FAIL (unknown field `ChangelogStatus` in `updateDTO`).

- [ ] **Step 3: Add the field and map it**

In `internal/httpapi/updates.go`, add to `type updateDTO struct` after `ChangelogText`:

```go
	ChangelogStatus string `json:"changelog_status"`
```

In both `handleListUpdates` and `handleListLastApplied`, add `ChangelogStatus: u.ChangelogStatus,` to the `updateDTO{...}` literal (next to `ChangelogURL`/`ChangelogText`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/updates.go internal/httpapi/updates_test.go
git commit -m "feat(httpapi): expose changelog_status on the updates DTO"
```

---

### Task 6: frontend renders the rate-limit hint

**Files:**
- Modify: `web/src/api/types.ts` (`Update.changelog_status?`)
- Modify: `web/src/components/Changelog.tsx` (optional `status` prop + rate-limit branch)
- Modify: `web/src/components/ReviewDrawer.tsx`, `web/src/components/ChangelogDrawer.tsx` (pass status)
- Test: `web/src/components/Changelog.test.tsx`, `web/src/components/ChangelogDrawer.test.tsx`

**Interfaces:**
- Consumes: `changelog_status` from `/api/updates` (Task 5).
- Produces: `Changelog` accepts `status?: string`; renders a rate-limit hint linking to `/settings/registries` when `markdown` is empty and `status === "rate_limited"`.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/components/Changelog.test.tsx`:

```tsx
test("shows a rate-limit hint with a settings link when status is rate_limited", () => {
  render(<Changelog markdown="" status="rate_limited" />);
  expect(screen.getByText(/rate limit/i)).toBeInTheDocument();
  const link = screen.getByRole("link", { name: /token in settings/i });
  expect(link).toHaveAttribute("href", "/settings/registries");
});

test("plain empty state when status is absent", () => {
  render(<Changelog markdown="" />);
  expect(screen.getByText(/no changelog available/i)).toBeInTheDocument();
  expect(screen.queryByText(/rate limit/i)).not.toBeInTheDocument();
});
```

Add to `web/src/components/ChangelogDrawer.test.tsx`:

```tsx
test("shows the rate-limit hint for a rate_limited update with no changelog", () => {
  render(
    <ChangelogDrawer
      update={{ ...update, changelog_text: "", changelog_url: "", changelog_status: "rate_limited" }}
      service={service}
      onClose={() => {}}
    />,
  );
  expect(screen.getByRole("link", { name: /token in settings/i })).toHaveAttribute("href", "/settings/registries");
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- Changelog`
Expected: FAIL (rate-limit hint / `status` prop not present).

- [ ] **Step 3a: Add the type field**

In `web/src/api/types.ts`, add to `interface Update` after `changelog_text: string;`:

```ts
  changelog_status?: string;
```

- [ ] **Step 3b: Add the `status` prop + branch to Changelog**

Replace the top of `web/src/components/Changelog.tsx`'s `Changelog` function (the signature and the `if (!markdown)` line) with:

```tsx
export function Changelog({ markdown, status }: { markdown: string; status?: string }) {
  if (!markdown) {
    if (status === "rate_limited") {
      return (
        <p className="text-sm opacity-60">
          GitHub rate limit reached. Changelog unavailable until the limit resets.{" "}
          <a href="/settings/registries" className="text-primary hover:underline">
            Add a GitHub token in Settings
          </a>{" "}
          to raise the limit.
        </p>
      );
    }
    return <p className="text-sm opacity-60">No changelog available.</p>;
  }
```

(Leave the rest of the component, the `<div className="changelog-body ...">` render, unchanged. Note: a plain `<a>`, not a router `Link`, so the component stays router-context-free and its tests need no provider.)

- [ ] **Step 3c: Pass status from the drawers**

In `web/src/components/ChangelogDrawer.tsx`, change the changelog render line to:

```tsx
          <Changelog markdown={update.changelog_text} status={update.changelog_url ? undefined : update.changelog_status} />
```

In `web/src/components/ReviewDrawer.tsx`, make the same change to its `<Changelog markdown={update.changelog_text} />` line:

```tsx
          <Changelog markdown={update.changelog_text} status={update.changelog_url ? undefined : update.changelog_status} />
```

(The `changelog_url ? undefined` guard suppresses the hint when a link is present, since a link is real content. `HistoryTimeline`'s `<Changelog>` call is left without a status, applied history does not surface the marker.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- Changelog`
Then the full web suite and typecheck: `cd web && npm test && npm run build`
Expected: PASS; build succeeds (types OK).

- [ ] **Step 5: Commit**

```bash
git add web/src/api/types.ts web/src/components/Changelog.tsx web/src/components/Changelog.test.tsx web/src/components/ChangelogDrawer.tsx web/src/components/ChangelogDrawer.test.tsx web/src/components/ReviewDrawer.tsx
git commit -m "feat(web): show GitHub rate-limit hint when a changelog is throttled"
```

---

### Task 7: docs (token tooltip + README)

**Files:**
- Modify: `web/src/components/settings/RegistriesSettings.tsx` (GitHub token `HelpTooltip` text)
- Modify: `README.md` (new "GitHub token and changelogs" section)

**Interfaces:** none (copy only).

- [ ] **Step 1: Expand the token tooltip**

In `web/src/components/settings/RegistriesSettings.tsx`, replace the GitHub token `HelpTooltip` text (currently "Personal access token used to fetch changelog and release notes from GitHub at a higher rate limit. Write-only, never displayed.") with:

```tsx
            <HelpTooltip text="Lifts the GitHub changelog rate limit from 60 to 5000 requests/hour (unauthenticated requests are throttled to 60/hour, which hides changelogs). Create one at github.com/settings/tokens as a classic token with no scopes checked (public release notes need none), then paste it here. Write-only, never displayed." />
```

- [ ] **Step 2: Add the README section**

In `README.md`, add a new section between the `## Configuration` block and `## Development`:

```markdown
## GitHub token and changelogs

dockbrr fetches changelogs and release notes from the GitHub Releases API. Without
a token those requests are anonymous and GitHub throttles them to 60 per hour, so
on a busy dashboard changelogs start showing "GitHub rate limit reached" instead of
release notes. Setting a token raises the limit to 5000 per hour.

To create one:

1. Go to https://github.com/settings/tokens and choose "Generate new token (classic)".
2. Name it (e.g. `dockbrr-changelog`) and pick an expiry.
3. Leave every scope unchecked. Reading public release notes needs no scopes.
4. Generate the token and copy it.
5. In dockbrr, open Settings, Registries, and paste it into "GitHub token", then Save.

The token is stored write-only and is never shown again. It is used only for
changelog and release-note reads.
```

- [ ] **Step 3: Verify build (typecheck picks up the tooltip change)**

Run: `cd web && npm run build`
Expected: build succeeds.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/settings/RegistriesSettings.tsx README.md
git commit -m "docs: explain the GitHub token and changelog rate limit"
```

---

## Final verification

Run the whole check suite before the whole-branch review:

```bash
CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
cd web && npm test && npm run build
```

Expected: all green. Then run the app (`mise run dev` or `mise run run`) and confirm: a GitHub-hosted update whose changelog was throttled shows the rate-limit hint with a working "Add a GitHub token in Settings" link to `/settings/registries`; after setting a token and hitting Check, the changelog fills and the hint disappears.

## Self-review notes

- Spec coverage: detection (T1), resolver signal (T2), persistence (T3), scan wiring (T4), API (T5), UI (T6), docs (T7). All spec sections mapped.
- The `ErrRateLimited` symbol is defined in T1 and consumed by name in T2 and T4; `SetChangelogStatus` is defined in T3 and consumed in T4; `updateDTO.ChangelogStatus`/`Update.changelog_status` names are consistent across T5 and T6.
- Auth-failure (401) path stays log-only by design (T1 test asserts it is not `ErrRateLimited`; the resolver logs it at `resolver.go`).
