# Current-version changelog for up-to-date services Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A service that is up-to-date with no update history shows the changelog of its current running version.

**Architecture:** When `detect` returns nil (up-to-date) and the service has zero update rows, `scan.CheckService` writes a synthetic `updates` row with `status='current'` (`from == to ==` current resolved version) and resolves its changelog through the existing chain. The dashboard's last-applied fallback is generalized to also surface `current` rows, so the row flows through the existing web decoration unchanged.

**Tech Stack:** Go 1.26 (CGO_ENABLED=0), SQLite via `internal/store`, React + TS + vitest (`web/`).

## Global Constraints

- `CGO_ENABLED=0` must hold; no new cgo dependencies.
- No shell / no Docker mutation in `scan` (read-only orchestrator, safety invariant 2).
- No em-dash punctuation (Unicode U+2014) in any code comment, doc, or copy string.
- `current` must never appear in `/api/updates` (`ListVisible`) nor be treated as pending/actionable: every existing filter/apply/badge path keys on `available`.
- Spec of record: `docs/dev/specs/2026-07-17-current-version-changelog-design.md`.

## Task summary table

| # | Task | Deps | Model | Reviewer | Plan section |
|---|------|------|-------|----------|--------------|
| 1 | Store: generalize `ListLastAppliedByService` to include `current` | - | sonnet | sonnet | Task 1 |
| 2 | Store: `HasAnyByService` helper | - | sonnet | sonnet | Task 2 |
| 3 | Scan: create + resolve synthetic `current` row on detect-nil + no history | 1,2 | sonnet | opus | Task 3 |
| 4 | Web: current-version wording on the changelog eye | 1 | sonnet | sonnet | Task 4 |

---

### Task 1: Store generalizes `ListLastAppliedByService` to include `current`

**Files:**
- Modify: `internal/store/updates.go:357-390` (`ListLastAppliedByService`)
- Test: `internal/store/updates_test.go`

**Interfaces:**
- Produces: `func (u *Updates) ListLastAppliedByService() ([]Update, error)` - unchanged signature. New behavior: returns the newest row per service among `status IN ('applied','current')`, with `applied` ranked above `current` for the same service regardless of timestamp.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/updates_test.go`:

```go
func TestListLastAppliedPrefersAppliedOverCurrent(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// A synthetic current-version row (from == to), then a real applied update.
	if _, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:cur",
		FromVersion: "1.0", ToVersion: "1.0", Tag: "1.0", Severity: "current", Status: "current",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0 current",
	}); err != nil {
		t.Fatal(err)
	}
	appliedID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:new",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.1", ChangelogText: "# 1.1 applied",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].ID != appliedID {
		t.Fatalf("got id %d, want applied %d (current must not win)", got[0].ID, appliedID)
	}
}

func TestListLastAppliedReturnsCurrentWhenOnly(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	curID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:cur",
		FromVersion: "1.0", ToVersion: "1.0", Tag: "1.0", Severity: "current", Status: "current",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0 current",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != curID {
		t.Fatalf("got %+v, want single current row id %d", got, curID)
	}
	if got[0].Status != "current" || got[0].ChangelogText != "# 1.0 current" {
		t.Fatalf("current row not carried faithfully: %+v", got[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run 'TestListLastApplied(PrefersAppliedOverCurrent|ReturnsCurrentWhenOnly)' -v`
Expected: FAIL - the current query filters `status='applied'`, so `TestListLastAppliedReturnsCurrentWhenOnly` returns 0 rows.

- [ ] **Step 3: Generalize the query**

In `internal/store/updates.go`, replace the body of `ListLastAppliedByService` (the `Query` call at lines 357-367) with:

```go
func (u *Updates) ListLastAppliedByService() ([]Update, error) {
	rows, err := u.db.Query(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates
		  WHERE status IN ('applied','current')
		    AND id = (SELECT id FROM updates u2
		               WHERE u2.service_id = updates.service_id
		                 AND u2.status IN ('applied','current')
		               -- applied outranks current for the same service (a real apply
		               -- beats a synthetic baseline); (status='current') sorts 0/1 so
		               -- applied(0) comes first, then newest-first within a tier.
		               ORDER BY (u2.status='current'),
		                        COALESCE(u2.applied_at, u2.detected_at) DESC, u2.id DESC LIMIT 1)
		  ORDER BY service_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var up Update
		var appliedAt sql.NullTime
		if err := rows.Scan(
			&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
			&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL,
			&up.ChangelogText, &up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
		); err != nil {
			return nil, err
		}
		if appliedAt.Valid {
			t := appliedAt.Time
			up.AppliedAt = &t
		}
		out = append(out, up)
	}
	return out, rows.Err()
}
```

Update the doc comment above it (currently "the newest APPLIED update per service") to say it returns the newest changelog-bearing non-open row per service among `applied` and `current`, applied outranking current.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run 'TestUpdatesListLastApplied|TestListLastApplied' -v`
Expected: PASS - new tests pass; the existing `TestUpdatesListLastAppliedByService`, `...ExcludesNonApplied`, `...MultipleServices` still pass (they use no `current` rows, so behavior is identical).

- [ ] **Step 5: Commit**

```bash
git add internal/store/updates.go internal/store/updates_test.go
git commit -m "feat(store): last-applied fallback also surfaces current-version rows"
```

---

### Task 2: Store `HasAnyByService` helper

**Files:**
- Modify: `internal/store/updates.go` (add method near `GetLatestOpenByService`, ~line 319)
- Test: `internal/store/updates_test.go`

**Interfaces:**
- Produces: `func (u *Updates) HasAnyByService(serviceID int64) (bool, error)` - true if the service has at least one row in `updates` (any status).

- [ ] **Step 1: Write the failing test**

Add to `internal/store/updates_test.go`:

```go
func TestHasAnyByService(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	has, err := u.HasAnyByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("HasAnyByService = true for a service with no update rows, want false")
	}

	if _, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Status: "dismissed",
	}); err != nil {
		t.Fatal(err)
	}
	has, err = u.HasAnyByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("HasAnyByService = false after inserting a row, want true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestHasAnyByService -v`
Expected: FAIL - `u.HasAnyByService undefined`.

- [ ] **Step 3: Implement the method**

In `internal/store/updates.go`, add:

```go
// HasAnyByService reports whether the service has any update row at all,
// regardless of status. scan uses it to gate synthetic current-version row
// creation: a current row is written only for a service with no history.
func (u *Updates) HasAnyByService(serviceID int64) (bool, error) {
	var one int
	err := u.db.QueryRow(
		`SELECT 1 FROM updates WHERE service_id=? LIMIT 1`, serviceID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
```

(`errors` and `database/sql` are already imported in this file.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestHasAnyByService -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/updates.go internal/store/updates_test.go
git commit -m "feat(store): add HasAnyByService for current-row creation gate"
```

---

### Task 3: Scan creates + resolves the synthetic `current` row

**Files:**
- Modify: `internal/scan/scan.go` (`CheckService`, the `upd == nil` branch at lines 100-111)
- Test: `internal/scan/scan_test.go`

**Interfaces:**
- Consumes: `Updates.HasAnyByService` (Task 2); `Updates.Upsert`, `Updates.SetChangelog`, `Updates.SetChangelogStatus`; `Images.GetByDigest`; `detect.SplitRef`; `changelog.Resolve` via the `Changelog` interface.
- Produces: no new exported symbol. Behavior: on `detect` nil + `HasAnyByService==false`, a `status='current'` row is upserted and its changelog resolved.

Notes for the implementer:
- The current version is resolved in order: `images.GetByDigest(repo, svc.CurrentDigest).ResolvedVersion`, then `svc.ImageVersion`, then the tag from `detect.SplitRef(svc.ImageRef)`. `ResolvedVersion` is the reverse-looked release version the UI already shows for floating tags.
- The synthetic row is `from == to == current digest`, `from_version == to_version == resolved version`, `tag ==` ref tag, `severity == "current"`, `status == "current"`. The resolver returns that version's own notes because `from == to` yields no span (`internal/changelog/github.go:159`).
- Changelog persistence mirrors the existing fresh-update branch exactly (rate-limit, miss, hit).
- If `svc.CurrentDigest == ""` (never happens for a running service, but guard), skip creation - there is nothing to key the row on.

- [ ] **Step 1: Write the failing tests**

Add to `internal/scan/scan_test.go`:

```go
func TestCheckServiceCreatesCurrentRowWhenUpToDateNoHistory(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	// The reverse-looked release version the UI shows for the running digest.
	_, _ = store.NewImages(db).Upsert(store.Image{
		Repo: "ghcr.io/acme/web", Digest: "sha256:cur",
		Labels: `{"org.opencontainers.image.source":"https://github.com/acme/web"}`,
	})
	_ = store.NewImages(db).SetResolvedVersion("ghcr.io/acme/web", "sha256:cur", "1.2.3")

	updates := store.NewUpdates(db)
	det := fakeDetector{upd: nil} // up to date
	cl := &fakeChangelog{text: "# 1.2.3 notes", url: "https://github.com/acme/web/releases/tag/1.2.3"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	rows, err := updates.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1 current row (%+v)", len(rows), rows)
	}
	r := rows[0]
	if r.Status != "current" {
		t.Fatalf("status = %q, want current", r.Status)
	}
	if r.FromDigest != "sha256:cur" || r.ToDigest != "sha256:cur" {
		t.Fatalf("digests = (%q,%q), want both sha256:cur", r.FromDigest, r.ToDigest)
	}
	if r.ToVersion != "1.2.3" || r.FromVersion != "1.2.3" {
		t.Fatalf("versions = (%q,%q), want 1.2.3", r.FromVersion, r.ToVersion)
	}
	if r.ChangelogText != "# 1.2.3 notes" || r.ChangelogURL == "" {
		t.Fatalf("changelog not persisted on current row: %+v", r)
	}
	// The resolver saw the running image's labels (repo resolution).
	if cl.gotLabels["org.opencontainers.image.source"] != "https://github.com/acme/web" {
		t.Fatalf("resolver got labels %v, want stored source label", cl.gotLabels)
	}
}

func TestCheckServiceSkipsCurrentRowWhenHistoryExists(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	updates := store.NewUpdates(db)
	// Pre-existing applied history: a current row must NOT be created.
	if _, err := updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:old", ToDigest: "sha256:cur",
		Tag: "1.2.3", Status: "applied",
	}); err != nil {
		t.Fatal(err)
	}

	det := fakeDetector{upd: nil}
	cl := &fakeChangelog{text: "should not be called for current", url: "https://x"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	var cnt int
	_ = db.QueryRow(`SELECT COUNT(*) FROM updates WHERE service_id=? AND status='current'`, sid).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("current rows = %d, want 0 (history existed)", cnt)
	}
}

func TestCheckServiceCurrentRowRateLimited(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	updates := store.NewUpdates(db)
	det := fakeDetector{upd: nil}
	cl := &fakeChangelog{err: changelog.ErrRateLimited}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	rows, _ := updates.ListLastAppliedByService()
	if len(rows) != 1 || rows[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("want single current row marked rate_limited, got %+v", rows)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scan/ -run 'TestCheckService(CreatesCurrentRow|SkipsCurrentRow|CurrentRowRateLimited)' -v`
Expected: FAIL - `CreatesCurrentRow` returns 0 rows (the `upd == nil` branch returns early without writing anything).

- [ ] **Step 3: Implement the current-row branch**

In `internal/scan/scan.go`, replace the `if upd == nil { ... }` block (lines 100-111) with:

```go
	if upd == nil {
		logger.Debugf("scan: service %d (%s) up to date", svc.ID, svc.Name)
		// Drift cleared (or never existed): forget any prior notify so a future,
		// different drift for this service fires the hint again.
		s.mu.Lock()
		delete(s.notifiedTo, serviceID)
		s.mu.Unlock()
		// A historyless, up-to-date service still deserves a changelog: write a
		// synthetic current-version row (from == to) so the dashboard can show
		// what the running version shipped. Only when the service has no update
		// row at all, so we never shadow real (open/applied/dismissed) history.
		if err := s.ensureCurrentChangelog(ctx, svc); err != nil {
			logger.Errorf("scan: ensure current changelog (service %d (%s)): %v", svc.ID, svc.Name, err)
		}
		return nil // up-to-date / unmonitorable
	}
```

Then add this method (place it right after `CheckService`):

```go
// ensureCurrentChangelog writes a synthetic status='current' update row for an
// up-to-date, historyless service and resolves its changelog. The row is
// from == to == the current running version, so the resolver returns that
// version's own release notes. It is inert everywhere pending/applied logic
// lives (those key on 'available'/'applied'); only the dashboard's last-applied
// fallback surfaces it. A missing/failed changelog is non-fatal.
func (s *Scanner) ensureCurrentChangelog(ctx context.Context, svc store.Service) error {
	if svc.CurrentDigest == "" {
		return nil // nothing to key the row on
	}
	has, err := s.updates.HasAnyByService(svc.ID)
	if err != nil {
		return err
	}
	if has {
		return nil // real (or prior current) history already provides a changelog
	}

	repo, tag := detect.SplitRef(svc.ImageRef)
	version := svc.ImageVersion
	var labels map[string]string
	if img, gerr := s.images.GetByDigest(repo, svc.CurrentDigest); gerr == nil {
		if img.ResolvedVersion != "" {
			version = img.ResolvedVersion
		}
		if img.Labels != "" {
			_ = json.Unmarshal([]byte(img.Labels), &labels)
		}
	}
	if version == "" {
		version = tag
	}

	row := store.Update{
		ServiceID:   svc.ID,
		FromDigest:  svc.CurrentDigest,
		ToDigest:    svc.CurrentDigest,
		FromVersion: version,
		ToVersion:   version,
		Tag:         tag,
		Severity:    "current",
		Status:      "current",
	}
	id, err := s.updates.Upsert(row)
	if err != nil {
		return err
	}
	row.ID = id

	remote := registry.RemoteImage{Ref: svc.ImageRef, Digest: svc.CurrentDigest, Labels: labels}
	text, url, err := s.changelog.Resolve(ctx, row, remote)
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(id, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (current row %d): %v", id, serr)
		}
	case err != nil:
		return err
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(id, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (current row %d): %v", id, serr)
		}
	}
	return nil
}
```

Confirm the file already imports `encoding/json`, `errors`, `dockbrr/internal/changelog`, `dockbrr/internal/detect`, `dockbrr/internal/registry`, `dockbrr/internal/store` (it does per the current head of `scan.go`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/scan/ -run 'TestCheckService' -v`
Expected: PASS - the three new tests pass and every existing `TestCheckService*` still passes (the up-to-date-with-history tests exercise `upd == nil` with pre-existing rows, which now hit the `has` short-circuit).

- [ ] **Step 5: Full package vet + test**

Run: `go vet ./... && go test ./internal/scan/ ./internal/store/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go
git commit -m "feat(scan): resolve current-version changelog for up-to-date services"
```

---

### Task 4: Web wording distinguishes current version from last applied

**Files:**
- Modify: `web/src/components/DashboardTable.tsx:152-165` (eye aria-label + tooltip)
- Test: `web/src/components/DashboardTable.test.tsx`

**Interfaces:**
- Consumes: `Update.status` (already on the `Update` type in `web/src/api/types.ts`; the DTO already serializes `status`). A `current`-sourced changelog cell has `changelog.status === "current"`.

Notes: no data-flow change. `joinRows`/`useDashboardRows` already pass a `current` row through `lastApplied` (Task 1 makes `/api/updates/last-applied` return it). Only the eye's copy changes: a `current`-sourced cell reads "Current version changelog", an applied one keeps "Last applied changelog".

- [ ] **Step 1: Write the failing test**

This file is msw + full-router integration style (no prop-based render). Mirror the existing test "changelog column falls back to the last applied update once nothing is pending" (DashboardTable.test.tsx:395), but serve the last-applied row with `status: "current"` and `from == to`. Add:

```tsx
test("changelog eye reads 'Current version' for an up-to-date service's current row", async () => {
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
              image_ref: "ghcr.io/acme/web:1.2.3",
              current_digest: "sha256:cur",
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
          id: 77,
          service_id: 10,
          from_digest: "sha256:cur",
          to_digest: "sha256:cur",
          from_version: "1.2.3",
          to_version: "1.2.3",
          tag: "1.2.3",
          severity: "current",
          changelog_url: "https://github.com/acme/web/releases/tag/1.2.3",
          changelog_text: "## 1.2.3\n\n- shipped",
          status: "current",
          detected_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();

  const button = await screen.findByRole("button", {
    name: /current version changelog for web/i,
  });
  await userEvent.click(button);
  await waitFor(() => expect(screen.getByText("1.2.3")).toBeInTheDocument());
  expect(screen.queryByRole("button", { name: /^apply$/i })).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- DashboardTable`
Expected: FAIL - the eye renders aria-label "Last applied changelog for app".

- [ ] **Step 3: Implement the wording**

In `web/src/components/DashboardTable.tsx`, inside `ActionsCell`, after the existing `isHistory` line (`const isHistory = !!changelog && changelog !== update;`) add:

```tsx
  const isCurrent = isHistory && changelog?.status === "current";
```

Then change the eye's `aria-label` (lines 152-156) to:

```tsx
              aria-label={
                isCurrent
                  ? `Current version changelog for ${service.name}`
                  : isHistory
                    ? `Last applied changelog for ${service.name}`
                    : `Changelog for ${service.name}`
              }
```

And the tooltip content (line 165) to:

```tsx
          <TooltipContent>
            {isCurrent ? "Current version changelog" : isHistory ? "Last applied changelog" : "Changelog"}
          </TooltipContent>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm test -- DashboardTable`
Expected: PASS - new test passes; existing DashboardTable tests (pending + last-applied wording) unaffected.

- [ ] **Step 5: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no output (clean). (Do NOT use `npx tsc` - the rtk proxy masks type errors; `tsc` directly or `npm run build` is the reliable check.)

- [ ] **Step 6: Commit**

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): label up-to-date changelog as current version"
```

---

## Final verification (after all tasks)

- [ ] `go vet ./... && go test ./...` - all Go packages green.
- [ ] `cd web && ./node_modules/.bin/tsc -b --noEmit && npm test` - types clean, vitest green.
- [ ] `mise run check` - full gate (go vet + go test + web vitest).
- [ ] Manual smoke (optional, per `/run`): a discovered up-to-date service with a GitHub-resolvable image shows a changelog eye that opens "Current version changelog".

## Known limitations (documented, out of scope)

- A `current` row, once written, blocks re-resolution (its presence makes `HasAnyByService` true), so a changelog miss (e.g. token added later) is not retried. Acceptable for v1; a later change may re-resolve an empty `current` row on manual Check.
- The `current` row's version is not refreshed if the running version changes without a dockbrr apply (spec defers this).
