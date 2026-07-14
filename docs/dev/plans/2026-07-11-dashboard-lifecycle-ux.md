# Dashboard & Lifecycle UX: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the seven-item dashboard/lifecycle UX batch from
`docs/dev/specs/2026-07-11-dashboard-lifecycle-ux-design.md`.

**Architecture:** Backend first (settings defaults, `gone_since` schema + store,
two new settings, discovery auto-prune, compose-file endpoint, events changelog
join), then frontend (pinned-digest truncation, gone-only header drop, visible-only
count, two settings controls + populated defaults, compose modal, history changelog).

**Tech Stack:** Go 1.26 (CGO-free), SQLite migrations, React + TS + Tailwind +
Radix + TanStack + vitest.

## Global Constraints

- CGO_ENABLED=0 must stay green: `CGO_ENABLED=0 go build ./...`.
- Compose/file access adds NO shell path (invariant 6). The compose-viewer
  endpoint uses `os.ReadFile` only.
- UI/API never mutate Docker (invariant 2). The auto-prune deletes STORE rows in
  discovery: not a Docker mutation.
- Changelog is rendered via react-markdown + rehype-sanitize, no
  `dangerouslySetInnerHTML`, no CDN (invariant 7).
- TS typecheck via `./node_modules/.bin/tsc -b --noEmit` then `npm run build`
  (NOT `npx tsc`). After any web build, restore the tracked placeholder:
  `git checkout -- internal/httpapi/dist/index.html`. Commit only `web/src/`
  sources, never built dist assets.
- Effective setting defaults (single source, used by both GET fallback and the
  consumer call sites: do not diverge): `poll_interval_seconds`=900,
  `concurrency`=2, `health_timeout_seconds`=120, `health_poll_seconds`=2,
  `cache_ttl_seconds`=600, `write_back_compose`=true (existing),
  `auto_remove_gone`=true, `gone_grace_seconds`=3600.

---

### Task 1: Settings GET returns effective numeric defaults (#5)

**Files:**
- Modify: `internal/httpapi/settings.go`
- Test: `internal/httpapi/settings_test.go`

**Interfaces:**
- Produces: `settingDefaults` map (`map[string]string`) in `settings.go`;
  `handleGetSettings` falls back to it for unset non-secret keys.

- [ ] **Step 1: Write the failing test**. A GET with an EMPTY settings store
  returns the defaults, not empty strings:

```go
func TestGetSettingsReturnsDefaultsWhenUnset(t *testing.T) {
	s := newTestServer(t) // reuse this file's existing server/store harness
	rr := doGet(t, s, "/api/settings") // reuse the file's existing GET helper
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"poll_interval_seconds": "900",
		"concurrency":           "2",
		"health_timeout_seconds": "120",
		"health_poll_seconds":   "2",
		"cache_ttl_seconds":     "600",
	}
	for k, v := range want {
		if got, _ := out[k].(string); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
```
(Match the actual helper names in `settings_test.go`; if none exist, follow the
sibling tests' request pattern.)

- [ ] **Step 2: Run to verify failure**: `go test ./internal/httpapi/ -run TestGetSettingsReturnsDefaults` → FAIL (empty strings).

- [ ] **Step 3: Implement**

```go
// settingDefaults is the single source of truth for editable-setting defaults.
// GET falls back to these when a key is unset so the UI shows real values, and
// the consumer call sites (settingDuration/settingInt in main.go) use the same
// numbers: keep them in sync (guarded by TestSettingDefaultsMatchConsumers).
var settingDefaults = map[string]string{
	"poll_interval_seconds":  "900",
	"concurrency":            "2",
	"health_timeout_seconds": "120",
	"health_poll_seconds":    "2",
	"cache_ttl_seconds":      "600",
	"auto_remove_gone":       "true",
	"gone_grace_seconds":     "3600",
	"write_back_compose":     "true",
}
```
In `handleGetSettings`, replace the final `else { out[spec.key] = v }` branch so
an unset non-secret key falls back to the default:
```go
} else {
	if v == "" {
		if def, ok := settingDefaults[spec.key]; ok {
			v = def
		}
	}
	out[spec.key] = v
}
```
Also collapse the existing `write_back_compose` special-case into this generic
path (its default now lives in `settingDefaults`). The bool default "true" is a
valid string value there. Verify the value emitted for `write_back_compose` is
still `"true"` when unset.

- [ ] **Step 4: Run tests**: `go test ./internal/httpapi/` → PASS. `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/httpapi/settings.go internal/httpapi/settings_test.go
git commit -m "fix(settings): GET returns effective defaults for unset keys"
```

---

### Task 2: `gone_since` schema + service/project store methods (#3 storage)

**Files:**
- Create: `internal/store/migrations/0005_service_gone_since.sql`
- Modify: `internal/store/services.go` (struct field, MarkGone, Upsert, reads, Delete)
- Modify: `internal/store/projects.go` (Delete)
- Test: `internal/store/services_test.go`, `internal/store/projects_test.go`

**Interfaces:**
- Produces: `store.Service.GoneSince *time.Time`; `Services.Delete(id int64) error`;
  `Projects.Delete(id int64) error`. `MarkGone` sets `gone_since` on transition
  only; `Upsert` clears it (present services are never gone).

- [ ] **Step 1: Migration**

`internal/store/migrations/0005_service_gone_since.sql`:
```sql
ALTER TABLE services ADD COLUMN gone_since TIMESTAMP;
```

- [ ] **Step 2: Failing tests**

```go
func TestMarkGoneSetsGoneSinceOnceThenPreserves(t *testing.T) {
	db := newDB(t)
	svs := store.NewServices(db)
	id := seedService(t, db) // reuse this file's seeding helper
	if err := svs.MarkGone(id); err != nil { t.Fatal(err) }
	got1, _ := svs.Get(id)
	if got1.GoneSince == nil { t.Fatal("gone_since not set on transition") }
	first := *got1.GoneSince
	if err := svs.MarkGone(id); err != nil { t.Fatal(err) } // still gone
	got2, _ := svs.Get(id)
	if got2.GoneSince == nil || !got2.GoneSince.Equal(first) {
		t.Fatalf("gone_since must be preserved on repeat MarkGone: %v -> %v", first, got2.GoneSince)
	}
}

func TestUpsertClearsGoneSince(t *testing.T) {
	db := newDB(t)
	svs := store.NewServices(db)
	id := seedService(t, db)
	_ = svs.MarkGone(id)
	// Re-upsert the same service (name/project) as present.
	present := seedServiceValue(id) // build a store.Service matching the seed
	if _, err := svs.Upsert(present); err != nil { t.Fatal(err) }
	got, _ := svs.Get(id)
	if got.GoneSince != nil { t.Fatalf("gone_since should be cleared on upsert, got %v", got.GoneSince) }
	if got.State == "gone" { t.Fatal("state should no longer be gone after present upsert") }
}

func TestServiceDeleteCascades(t *testing.T) {
	db := newDB(t)
	id := seedService(t, db)
	if err := store.NewServices(db).Delete(id); err != nil { t.Fatal(err) }
	if _, err := store.NewServices(db).Get(id); !errors.Is(err, store.ErrServiceNotFound) {
		t.Fatalf("service should be gone after Delete, err=%v", err)
	}
}

func TestProjectDelete(t *testing.T) {
	db := newDB(t)
	pid := seedProject(t, db)
	if err := store.NewProjects(db).Delete(pid); err != nil { t.Fatal(err) }
}
```
(Use the real seeding helpers/`ErrServiceNotFound` name from the store package;
if `ErrServiceNotFound` doesn't exist, assert on the `Get` error the package
actually returns.)

- [ ] **Step 3: Run red**: `go test ./internal/store/ -run 'GoneSince|ServiceDelete|ProjectDelete'` → FAIL.

- [ ] **Step 4: Implement**

`Service` struct: add `GoneSince *time.Time` (after `State`).

`MarkGone`: set gone_since on transition, preserve if already gone:
```go
func (s *Services) MarkGone(id int64) error {
	_, err := s.db.Exec(
		`UPDATE services SET state='gone',
		   gone_since=COALESCE(gone_since, CURRENT_TIMESTAMP),
		   updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`, id)
	return err
}
```

`Upsert`: add `gone_since` to the INSERT column list set to `NULL`, and to the
`ON CONFLICT ... DO UPDATE` set `gone_since=NULL` (a present service is never
gone). Add `gone_since` to every SELECT/Scan (`Get`, `ListByProject`, `List`)
scanning into a `sql.NullTime`, mapping to `*time.Time` on the struct (mirror how
an existing nullable is scanned; if none, use `var gs sql.NullTime` then
`if gs.Valid { t := gs.Time; sv.GoneSince = &t }`).

`Services.Delete`:
```go
// Delete removes a service row. FK cascade removes its updates, snapshots, and
// events (ON DELETE CASCADE in the schema).
func (s *Services) Delete(id int64) error {
	_, err := s.db.Exec(`DELETE FROM services WHERE id=?`, id)
	return err
}
```

`Projects.Delete`:
```go
// Delete removes a project row (its services cascade).
func (p *Projects) Delete(id int64) error {
	_, err := p.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}
```

- [ ] **Step 5: Run green**: `go test ./internal/store/` → PASS. `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 6: Commit**
```bash
git add internal/store/migrations/0005_service_gone_since.sql internal/store/services.go internal/store/projects.go internal/store/services_test.go internal/store/projects_test.go
git commit -m "feat(store): service gone_since + Service/Project Delete"
```

---

### Task 3: `auto_remove_gone` + `gone_grace_seconds` settings (#3 config)

**Files:**
- Modify: `internal/httpapi/settings.go` (whitelist the two keys)
- Test: `internal/httpapi/settings_test.go`

**Interfaces:**
- Consumes: `settingDefaults` (Task 1). Produces: the two keys are GET/PUT-able
  and GET returns their defaults when unset.

- [ ] **Step 1: Failing test**, GET returns `auto_remove_gone="true"` and
  `gone_grace_seconds="3600"` when unset; PUT accepts them.
```go
func TestGoneSettingsDefaultsAndAccept(t *testing.T) {
	s := newTestServer(t)
	out := getSettings(t, s) // reuse helper
	if out["auto_remove_gone"] != "true" { t.Errorf("auto_remove_gone default = %v", out["auto_remove_gone"]) }
	if out["gone_grace_seconds"] != "3600" { t.Errorf("gone_grace_seconds default = %v", out["gone_grace_seconds"]) }
	putSettings(t, s, map[string]string{"auto_remove_gone": "false", "gone_grace_seconds": "600"})
	out2 := getSettings(t, s)
	if out2["auto_remove_gone"] != "false" || out2["gone_grace_seconds"] != "600" {
		t.Errorf("PUT not persisted: %v", out2)
	}
}
```

- [ ] **Step 2: Run red** → FAIL (keys absent from whitelist → not emitted/accepted).

- [ ] **Step 3: Implement**: add to `settingKeys`:
```go
	{"auto_remove_gone", false},
	{"gone_grace_seconds", false},
```
Their defaults already live in `settingDefaults` (Task 1). `auto_remove_gone` is
a bool stored as "true"/"false" string (consumed via `GetBoolDefault`);
`gone_grace_seconds` is an int-seconds string.

- [ ] **Step 4: Run green**: `go test ./internal/httpapi/` → PASS. Build.

- [ ] **Step 5: Commit**
```bash
git add internal/httpapi/settings.go internal/httpapi/settings_test.go
git commit -m "feat(settings): auto_remove_gone + gone_grace_seconds keys"
```

---

### Task 4: Discovery auto-prunes gone services & empty projects (#3 feature)

**Files:**
- Modify: `internal/discovery/discovery.go` (Reconciler settings dep + prune pass)
- Modify: `cmd/dockbrr/main.go` (wire settings into `NewReconciler`)
- Test: `internal/discovery/discovery_test.go`

**Interfaces:**
- Consumes: `Services.Delete`, `Projects.Delete`, `Service.GoneSince` (Task 2);
  `store.Settings.GetBoolDefault` + an int settings read. The Reconciler gains a
  `settings *store.Settings` field.

- [ ] **Step 1: Failing tests** (match the discovery test harness, reuse its
  fake collector + store):
  - service gone with `gone_since` older than grace + `auto_remove_gone=true` →
    deleted after Reconcile.
  - service gone but within grace → NOT deleted.
  - `auto_remove_gone=false` → gone service kept regardless of age.
  - a DISCOVERED project left with zero services after prune → deleted; a MANUAL
    empty project → preserved.

  To make a service "gone past grace" deterministically without sleeping: after
  the collector stops returning it (so Reconcile MarkGones it), back-date its
  `gone_since` directly via a store `Exec` in the test
  (`UPDATE services SET gone_since=? WHERE id=?` with a time older than grace),
  then run Reconcile again and assert deletion. (Do NOT use real sleeps.)

- [ ] **Step 2: Run red** → FAIL.

- [ ] **Step 3: Implement**

Add `settings *store.Settings` to `Reconciler` + a param to `NewReconciler`
(update `cmd/dockbrr/main.go` call site to pass the settings store).

After the existing mark-gone pass (`discovery.go:308`), add a prune pass:
```go
// Auto-prune: hard-delete services that have been gone longer than the grace,
// then discovered projects left empty. Off by default-safe: only runs when the
// user opted in (default on). gone_grace is read live each cycle.
if r.settings != nil && r.settings.GetBoolDefault("auto_remove_gone", true) {
	grace := time.Duration(settingIntDefault(r.settings, "gone_grace_seconds", 3600)) * time.Second
	cutoff := time.Now().UTC().Add(-grace)
	all, err := r.projects.List()
	if err != nil { return changed, err }
	for _, p := range all {
		svcs, err := r.services.ListByProject(p.ID)
		if err != nil { return changed, err }
		for _, sv := range svcs {
			if sv.State == "gone" && sv.GoneSince != nil && sv.GoneSince.Before(cutoff) {
				if err := r.services.Delete(sv.ID); err != nil { return changed, err }
				changed = true
			}
		}
		// Re-check emptiness after deletions; only discovered projects are pruned.
		if p.Source == "discovered" {
			remaining, err := r.services.ListByProject(p.ID)
			if err != nil { return changed, err }
			if len(remaining) == 0 {
				if err := r.projects.Delete(p.ID); err != nil { return changed, err }
				changed = true
			}
		}
	}
}
```
Add a small local helper `settingIntDefault(s *store.Settings, key string, def int) int`
in the discovery package (parse `s.Get(key)`, fall back to `def`), or, if the
store already exposes an int accessor, use that. Keep it minimal.

Ensure the prune runs INSIDE `Reconcile` after mark-gone and sets `changed=true`
when it deletes anything (so `reconciled` is published).

- [ ] **Step 4: Run green**: `go test ./internal/discovery/` → PASS. `CGO_ENABLED=0 go build ./...`; `go vet ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/discovery/discovery.go cmd/dockbrr/main.go internal/discovery/discovery_test.go
git commit -m "feat(discovery): auto-prune gone services + empty projects past grace"
```

---

### Task 5: Compose-file viewer endpoint (#4 backend)

**Files:**
- Modify: `internal/httpapi/projects.go` (handler) + `internal/httpapi/server.go` (route)
- Test: `internal/httpapi/projects_test.go`

**Interfaces:**
- Produces: `GET /api/projects/{id}/compose` → `{"files":[{"path":"…","content":"…","error":"…"}]}`.

- [ ] **Step 1: Failing test**: seed a discovered project with a config file on
  disk (`t.TempDir()` + write a compose file, store the project with that
  `config_files`); GET returns the file's content. Plus an auth test (401 through
  the router, mirroring existing `TestProjectsUnauthenticated*`).

- [ ] **Step 2: Run red** → FAIL (route/handler absent).

- [ ] **Step 3: Implement**

Handler (read each `proj.ConfigFiles` path):
```go
type composeFileDTO struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleProjectCompose(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	proj, err := s.deps.Projects.Get(id) // use the store's project Get (verify name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err)
		return
	}
	files := make([]composeFileDTO, 0, len(proj.ConfigFiles))
	for _, p := range proj.ConfigFiles {
		f := composeFileDTO{Path: p}
		if b, rerr := os.ReadFile(p); rerr != nil {
			f.Error = rerr.Error()
		} else {
			f.Content = string(b)
		}
		files = append(files, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
```
(If `Projects` has no `Get`, add a minimal `Projects.Get(id)` or reuse `List` +
find; prefer a real `Get`.) Register in the auth/CSRF group in `server.go`
alongside the other project routes:
```go
r.Get("/api/projects/{id}/compose", s.handleProjectCompose)
```

- [ ] **Step 4: Run green**: `go test ./internal/httpapi/` → PASS. Build.

- [ ] **Step 5: Commit**
```bash
git add internal/httpapi/projects.go internal/httpapi/server.go internal/httpapi/projects_test.go
git commit -m "feat(api): GET /projects/{id}/compose returns config file contents"
```

---

### Task 6: Events carry the matching update's changelog (#7 backend)

**Files:**
- Modify: `internal/store/events.go` (Event struct + ListByService join)
- Modify: `internal/httpapi/services.go` (events DTO changelog fields)
- Test: `internal/store/events_test.go`

**Interfaces:**
- Produces: `store.Event.ChangelogURL`, `store.Event.ChangelogText`; the events
  DTO gains `changelog_url` / `changelog_text`.

- [ ] **Step 1: Failing test**: seed a service, an update row (with
  `changelog_url`/`changelog_text`, status `applied`) at digest D, and a
  `succeeded` event with `to_digest = D`; `ListByService` returns that event with
  the changelog populated. An event whose `to_digest` matches no update → empty
  changelog fields.

- [ ] **Step 2: Run red** → FAIL.

- [ ] **Step 3: Implement**: add `ChangelogURL string` + `ChangelogText string`
  to `Event`; change `ListByService`'s query to LEFT JOIN and select them:
```go
SELECT e.id, e.service_id, e.kind, e.from_digest, e.to_digest, e.ref_job_id,
       e.message, e.created_at,
       COALESCE(u.changelog_url,''), COALESCE(u.changelog_text,'')
  FROM service_events e
  LEFT JOIN updates u
    ON u.service_id = e.service_id AND u.to_digest = e.to_digest
 WHERE e.service_id=? ORDER BY e.id DESC
```
(Adjust the exact column list to the real `service_events` columns, read the
current SELECT first and preserve its order/fields, appending the two changelog
columns.) Scan the two extra columns into the new fields. The
`UNIQUE(service_id, to_digest)` on updates makes this 1:1.

In `internal/httpapi/services.go`, add `ChangelogURL string \`json:"changelog_url"\``
and `ChangelogText string \`json:"changelog_text"\`` to the event DTO and populate
from the store Event.

- [ ] **Step 4: Run green**: `go test ./internal/store/ ./internal/httpapi/` → PASS. Build.

- [ ] **Step 5: Commit**
```bash
git add internal/store/events.go internal/httpapi/services.go internal/store/events_test.go
git commit -m "feat(store): events carry matching update changelog"
```

---

### Task 7: Frontend dashboard fixes: pinned digest, gone-only header, visible count (#1/#2/#6)

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (pinned cell → DigestShort)
- Modify: `web/src/hooks/useDashboardRows.ts` (drop gone-only project header)
- Modify: `web/src/components/DashboardStats.tsx` (count only visible)
- Test: the corresponding `.test.tsx` files

**Interfaces:** consumes the existing `DigestShort` component and the filtered
rows the table already computes.

- [ ] **Step 1: Failing tests**
  - a Pinned service row renders the digest via `DigestShort` (short 12-hex), not
    the full `sha256:…` string.
  - `joinRows`: a project whose only service is Gone produces NO project header
    when `showRemoved` is false (and DOES when true).
  - DashboardStats "updates available" count reflects only visible (filtered)
    services: an update on a Gone service is not counted when show-removed is off.

- [ ] **Step 2: Run red**: `cd web && npm test -- 'DashboardTable|useDashboardRows|DashboardStats'` → FAIL.

- [ ] **Step 3: Implement**
  - DashboardTable pinned cell: where the pinned ref/digest is shown, render
    `<DigestShort value={...} />` instead of the raw string (read the current
    pinned rendering; keep the tag visible, truncate only the digest).
  - `useDashboardRows.joinRows`: after filtering a project's services, if the
    resulting visible-services list is empty, omit the project header (extend the
    existing empty-header drop to include the all-gone-hidden case).
  - DashboardStats: compute the "updates available" count from the same
    visible/filtered row set the table uses (not the raw services list). If the
    count is derived from a prop, pass the filtered set; keep the tile's other
    counts consistent.

- [ ] **Step 4: Run green**: `npm test -- 'DashboardTable|useDashboardRows|DashboardStats'` → PASS; then `./node_modules/.bin/tsc -b --noEmit && npm run build`; `git checkout -- internal/httpapi/dist/index.html`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/DashboardTable.tsx web/src/hooks/useDashboardRows.ts web/src/components/DashboardStats.tsx web/src/components/DashboardTable.test.tsx web/src/hooks/useDashboardRows.test.tsx web/src/components/DashboardStats.test.tsx
git commit -m "fix(web): short pinned digest, hide gone-only projects, visible-only update count"
```

---

### Task 8: Frontend settings: auto-remove controls + populated defaults (#3 UI / #5)

**Files:**
- Modify: `web/src/components/settings/GeneralSettings.tsx`
- Modify: `web/src/api/types.ts` (Settings gains `auto_remove_gone`, `gone_grace_seconds`)
- Test: `web/src/components/settings/GeneralSettings.test.tsx`

**Interfaces:** consumes GET `/api/settings` (now returns defaults + the two new
keys from Tasks 1/3). Reuses the existing dirty-indicator + diff-save.

- [ ] **Step 1: Failing test**: with a fixture that includes populated numeric
  defaults (`poll_interval_seconds: "900"`, etc.) and `auto_remove_gone: "true"`,
  `gone_grace_seconds: "3600"`: the number inputs show the default values (not
  empty), the "Auto-remove gone services" Switch is checked, and toggling it (or
  editing the grace) shows "Unsaved changes".

- [ ] **Step 2: Run red**: `cd web && npm test -- GeneralSettings` → FAIL.

- [ ] **Step 3: Implement**
  - Add `"auto_remove_gone"` and `"gone_grace_seconds"` to `EditableKey`,
    `EDITABLE_KEYS`, and the `setForm` init.
  - Add a Switch "Auto-remove gone services & empty projects"
    (`checked={form.auto_remove_gone === "true"}`, sets "true"/"false") mirroring
    the air-gap/write-back switches.
  - Add `gone_grace_seconds` to `NUMBER_FIELDS` (label e.g. "Gone-removal grace
    (seconds)") so it renders as a number input via the existing map.
  - `web/src/api/types.ts`: add `auto_remove_gone: string;` and
    `gone_grace_seconds: string;` to `Settings`.
  - (No code change needed for #5 populated defaults beyond the backend Task 1,     verify the numeric fields now show values; the test asserts it.)

- [ ] **Step 4: Run green**: `npm test -- GeneralSettings` → PASS; `./node_modules/.bin/tsc -b --noEmit && npm run build`; `git checkout -- internal/httpapi/dist/index.html`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/settings/GeneralSettings.tsx web/src/api/types.ts web/src/components/settings/GeneralSettings.test.tsx
git commit -m "feat(web): auto-remove settings controls + populated defaults"
```

---

### Task 9: Frontend compose-file modal (#4)

**Files:**
- Create: `web/src/components/ComposeModal.tsx`
- Modify: the project header component (where project name/actions render, likely
  `web/src/components/DashboardTable.tsx` project header row) to add the trigger
- Modify: `web/src/hooks/queries.ts` (a `useProjectCompose(id)` query) + `web/src/api/types.ts`
- Test: `web/src/components/ComposeModal.test.tsx`

**Interfaces:** consumes `GET /api/projects/{id}/compose` (Task 5). Uses the
existing `Dialog` primitive.

- [ ] **Step 1: Failing test**: with the compose endpoint mocked (msw) returning
  two files, opening the modal renders each file's `path` label and its `content`
  in a `<pre>`; a file with an `error` shows the error text.

- [ ] **Step 2: Run red**: `cd web && npm test -- ComposeModal` → FAIL.

- [ ] **Step 3: Implement**
  - `useProjectCompose(id)` in `queries.ts`: GET `/api/projects/{id}/compose`,
    typed `{ files: { path: string; content: string; error?: string }[] }` (add
    the type to `types.ts`). Query enabled only when the modal is open (pass an
    `enabled` flag) to avoid fetching every project's file eagerly.
  - `ComposeModal.tsx`: a `Dialog` (centered) titled with the project name;
    lists each file as a labelled, scrollable `<pre className="overflow-auto ...">`
    block (wrap in `overflow-x-auto` so wide lines scroll inside the block, not
    the page). Read-only. Loading + error states.
  - Add a small trigger (button/link, e.g. a "Compose" action) on the project
    header row that opens the modal for that project id.

- [ ] **Step 4: Run green**: `npm test -- ComposeModal` → PASS; `./node_modules/.bin/tsc -b --noEmit && npm run build`; `git checkout -- internal/httpapi/dist/index.html`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/ComposeModal.tsx web/src/components/DashboardTable.tsx web/src/hooks/queries.ts web/src/api/types.ts web/src/components/ComposeModal.test.tsx
git commit -m "feat(web): compose-file viewer modal on projects"
```

---

### Task 10: Frontend history changelog affordance (#7)

**Files:**
- Modify: `web/src/components/HistoryTimeline.tsx` (changelog affordance)
- Modify: `web/src/api/types.ts` (service event type gains `changelog_url`, `changelog_text`)
- Test: `web/src/components/HistoryTimeline.test.tsx`

**Interfaces:** consumes the events DTO changelog fields (Task 6). Reuses the
existing `Changelog` component (react-markdown + rehype-sanitize).

- [ ] **Step 1: Failing test**. A history entry with `changelog_text` (or
  `changelog_url`) renders a "Changelog" affordance; activating it shows the
  sanitized changelog (reuse the existing `Changelog` render assertions). An entry
  with neither field shows no affordance. Include an XSS-shaped `changelog_text`
  and assert the script does not execute / is stripped (mirror the ReviewDrawer
  changelog XSS test).

- [ ] **Step 2: Run red**: `cd web && npm test -- HistoryTimeline` → FAIL.

- [ ] **Step 3: Implement**
  - Add `changelog_url?: string` and `changelog_text?: string` to the service
    event type in `types.ts`.
  - In `HistoryTimeline.tsx`, when an entry has a changelog, render a "Changelog"
    button/disclosure that shows the existing `Changelog` component with
    `markdown={entry.changelog_text ?? ""}` and the `changelog_url` (same props
    the review drawer passes). No `dangerouslySetInnerHTML`. When only a URL is
    present, the `Changelog` component's link path handles it.

- [ ] **Step 4: Run green**: `npm test -- HistoryTimeline` → PASS; `./node_modules/.bin/tsc -b --noEmit && npm run build`; `git checkout -- internal/httpapi/dist/index.html`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/HistoryTimeline.tsx web/src/api/types.ts web/src/components/HistoryTimeline.test.tsx
git commit -m "feat(web): changelog affordance in service history"
```

---

## Final verification (after all tasks)

- `CGO_ENABLED=0 go build ./...`; `go vet ./... && go test ./...`, green.
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm test && npm run build`, green.
- `git checkout -- internal/httpapi/dist/index.html`: restore placeholder.
- Manual smoke (live Docker): populated settings defaults; toggle auto-remove off
  → gone service persists; `docker rm` a smoke container, back-date its
  `gone_since`, reconcile → service + empty project pruned; compose modal shows
  the file (incl. a prior write-back edit); apply an update then open the
  service's history → its changelog is viewable.
