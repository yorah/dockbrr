# Default Auto-Update For New Discovered Projects Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global setting, `default_auto_update_enabled` (default `false`,
no behavior change out of the box), that controls whether newly discovered
compose stacks start with auto-update on or off. Existing projects and
manually-added projects are never affected.

**Architecture:** `store.Projects.Upsert`'s INSERT gains the
`auto_update_enabled` column (currently silently defaulted by the schema);
`discovery.Reconciler` sets it from the new setting only when inserting a
brand-new discovered project; the setting is exposed through the existing
settings whitelist and a new UI toggle.

**Tech Stack:** Go (`internal/store`, `internal/discovery`, `internal/httpapi`), React/TS (`web/src/api/types.ts`, `web/src/components/settings/UpdatesSettings.tsx`).

## Global Constraints

- New setting key: `default_auto_update_enabled`, default `"false"` (string, like all other bool settings in this codebase).
- Not added to `restartRequired`: must be hot-reloadable, matching `auto_remove_gone`.
- The `ON CONFLICT` clause in `Projects.Upsert` must NOT change, existing projects must never have `auto_update_enabled` reset by a later reconcile.
- Manual project creation (`handleCreateProject`) is explicitly out of scope. It must keep inserting `auto_update_enabled=false` regardless of this setting.
- `go build ./...`, `go vet ./...`, `go test ./...` must stay green after every Go task.
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build` and `npm test` must stay green after every frontend task. Use `tsc` directly, NOT `npx tsc` (a shell hook falsely reports "No errors" for `npx tsc` in this environment).

---

### Task 1: Store: persist `auto_update_enabled` on insert

**Files:**
- Modify: `internal/store/projects.go:48-60` (`Projects.Upsert`)
- Test: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: existing `store.Project.AutoUpdateEnabled bool` field (already defined at `internal/store/projects.go:20`): no change to the struct.
- Produces: `Upsert` now persists `pr.AutoUpdateEnabled` on the INSERT path. Task 2 relies on this: setting `AutoUpdateEnabled: true` on a `store.Project{}` passed to `Upsert` will now actually stick for a brand-new row.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/projects_test.go` (place it right after
`TestProjectsUpsertByNaturalKeyPreservesUserColumns`):

```go
func TestProjectsUpsertInsertsAutoUpdateEnabled(t *testing.T) {
	db := openTempStore(t)
	p := store.NewProjects(db)

	id, err := p.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web", Source: "discovered",
		AutoUpdateEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoUpdateEnabled {
		t.Fatal("auto_update_enabled = false, want true (INSERT must persist pr.AutoUpdateEnabled)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestProjectsUpsertInsertsAutoUpdateEnabled -v`
Expected: FAIL: `auto_update_enabled = false, want true` (the current INSERT statement omits the column, so the schema's `DEFAULT 0` wins regardless of `pr.AutoUpdateEnabled`).

- [ ] **Step 3: Write minimal implementation**

In `internal/store/projects.go`, replace the `Upsert` query (the body between
`var id int64` and `.Scan(&id)`):

```go
	var id int64
	err = p.db.QueryRow(
		`INSERT INTO projects (host_id, kind, name, working_dir, config_files, source, auto_update_enabled, last_synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host_id, name) DO UPDATE SET
		   kind           = excluded.kind,
		   working_dir    = excluded.working_dir,
		   config_files   = excluded.config_files,
		   last_synced_at = excluded.last_synced_at
		 RETURNING id`,
		pr.HostID, pr.Kind, pr.Name, pr.WorkingDir, string(cfJSON), source, pr.AutoUpdateEnabled, pr.LastSyncedAt,
	).Scan(&id)
	return id, err
```

Note: the `ON CONFLICT ... DO UPDATE SET` list is unchanged, it still does
not mention `auto_update_enabled`, so re-upserting an existing project never
touches the column.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/... -run TestProjectsUpsertInsertsAutoUpdateEnabled -v`
Expected: PASS

- [ ] **Step 5: Run the full store suite to confirm no regression**

Run: `go test ./internal/store/... -v 2>&1 | tail -30`
Expected: all tests pass, including
`TestProjectsUpsertByNaturalKeyPreservesUserColumns` (the conflict-preserve
case): it must still show the out-of-band-enabled project keeping
`auto_update_enabled=true` after a discovery re-upsert with
`AutoUpdateEnabled` left at its Go zero value (`false`), because that upsert
takes the `ON CONFLICT` branch, not the INSERT branch.

- [ ] **Step 6: Build, vet, full suite**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, vet clean, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/store/projects.go internal/store/projects_test.go
git commit -m "$(cat <<'EOF'
feat(store): persist auto_update_enabled on project insert

Upsert's INSERT previously omitted this column, so it silently relied
on the schema's DEFAULT 0 regardless of what the caller set on
Project.AutoUpdateEnabled. The ON CONFLICT path is untouched, so
existing projects are still never reset.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Discovery: wire the new-project default from settings

**Files:**
- Modify: `internal/discovery/discovery.go:248-256` (inside `Reconcile`, the non-manual branch that upserts a brand-new discovered project)
- Test: `internal/discovery/discovery_test.go`

**Interfaces:**
- Consumes: Task 1's fix (`Upsert` now persists `AutoUpdateEnabled` on INSERT). Also consumes `r.settings *store.Settings` (already a `Reconciler` field, set via `NewReconciler`'s existing `settings` parameter. No signature change) and its existing method `GetBoolDefault(key string, def bool) bool`.
- Produces: nothing new consumed by later tasks: Tasks 3 and 4 touch unrelated files (the settings whitelist and the frontend) and do not depend on this task's code, only on agreeing on the same setting-key string `"default_auto_update_enabled"`.

- [ ] **Step 1: Write the failing test**

Add to `internal/discovery/discovery_test.go` (place it after
`TestReconcilePopulatesStore`):

```go
func TestReconcileNewProjectRespectsDefaultAutoUpdateSetting(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("default_auto_update_enabled", "true"); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:          "c1",
				Project:     "app",
				Service:     "web",
				WorkingDir:  "/srv/app",
				ConfigFiles: []string{"docker-compose.yml"},
				Name:        "app_web_1",
				ImageRef:    "nginx:latest",
				State:       "running",
			},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "app" {
		t.Fatalf("projects = %+v", all)
	}
	if !all[0].AutoUpdateEnabled {
		t.Fatal("AutoUpdateEnabled = false, want true (default_auto_update_enabled=true must apply to a brand-new discovered project)")
	}
}

func TestReconcileNewProjectDefaultsAutoUpdateOffWhenUnset(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db) // default_auto_update_enabled left unset

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:          "c1",
				Project:     "app",
				Service:     "web",
				WorkingDir:  "/srv/app",
				ConfigFiles: []string{"docker-compose.yml"},
				Name:        "app_web_1",
				ImageRef:    "nginx:latest",
				State:       "running",
			},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("projects = %+v", all)
	}
	if all[0].AutoUpdateEnabled {
		t.Fatal("AutoUpdateEnabled = true, want false (shipped default must stay off)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail/pass appropriately**

Run: `go test ./internal/discovery/... -run TestReconcileNewProjectRespectsDefaultAutoUpdateSetting -v`
Expected: FAIL: `AutoUpdateEnabled = false, want true` (nothing reads the setting yet).

Run: `go test ./internal/discovery/... -run TestReconcileNewProjectDefaultsAutoUpdateOffWhenUnset -v`
Expected: PASS already (current code never sets `AutoUpdateEnabled`, so it's the Go zero value `false`). This test exists to pin the shipped-default behavior going forward, not to catch a current bug.

- [ ] **Step 3: Write minimal implementation**

In `internal/discovery/discovery.go`, in the non-manual branch inside
`Reconcile` (the `else` block that currently reads exactly as follows):

```go
		} else {
			var err error
			pid, err = r.projects.Upsert(store.Project{
				HostID:       r.hostID,
				Kind:         g.Kind,
				Name:         g.Name,
				WorkingDir:   g.WorkingDir,
				ConfigFiles:  g.ConfigFiles,
				Source:       "discovered",
				LastSyncedAt: &now,
			})
			if err != nil {
				return false, err
			}
		}
```

replace it with:

```go
		} else {
			var err error
			pid, err = r.projects.Upsert(store.Project{
				HostID:            r.hostID,
				Kind:              g.Kind,
				Name:              g.Name,
				WorkingDir:        g.WorkingDir,
				ConfigFiles:       g.ConfigFiles,
				Source:            "discovered",
				AutoUpdateEnabled: r.settings != nil && r.settings.GetBoolDefault("default_auto_update_enabled", false),
				LastSyncedAt:      &now,
			})
			if err != nil {
				return false, err
			}
		}
```

- [ ] **Step 4: Run both tests to verify they pass**

Run: `go test ./internal/discovery/... -run TestReconcileNewProject -v`
Expected: both PASS.

- [ ] **Step 5: Run the full discovery suite to confirm no regression**

Run: `go test ./internal/discovery/... -v 2>&1 | tail -40`
Expected: all tests pass, including `TestReconcilePopulatesStore` (which calls
`NewReconciler(fc, projects, services, 1, nil, nil)` with a nil `settings`, confirms the `r.settings != nil &&` guard prevents a nil-pointer panic and
still yields `false`).

- [ ] **Step 6: Build, vet, full suite**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, vet clean, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/discovery/discovery.go internal/discovery/discovery_test.go
git commit -m "$(cat <<'EOF'
feat(discovery): apply default_auto_update_enabled to new projects

Only the brand-new-project branch of Reconcile is affected, the
isManual reuse branch and any later re-upsert of an existing project
never touch this field (store.Upsert's ON CONFLICT already excludes
it).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Settings API: whitelist the new setting

**Files:**
- Modify: `internal/httpapi/settings.go:20-57` (`settingDefaults`, `settingKeys`)
- Test: `internal/httpapi/settings_test.go`

**Interfaces:**
- Consumes: nothing from Tasks 1-2 (independent of them; this task only makes the setting key visible/editable through the `/api/settings` GET/PUT endpoints. The underlying `store.Settings.GetBoolDefault` call in Task 2 works against the raw key/value store regardless of this whitelist).
- Produces: the setting key string `"default_auto_update_enabled"` that Task 4's frontend types/UI must match exactly.

- [ ] **Step 1: Write the failing test**

Add to `internal/httpapi/settings_test.go`, extending the existing
`TestSettingDefaultsMatchConsumers`'s `want` map (it already has a comment
block for discovery-side consumers: add this entry there):

```go
		// Discovery-side consumers (internal/discovery/discovery.go auto-prune
		// pass). Pinned here too so drift from the discovery.go literals is
		// caught, not just the main.go-consumed defaults above.
		"auto_remove_gone":   "true",
		"gone_grace_seconds": "3600",
		// New-project auto-update default (internal/discovery/discovery.go
		// Reconcile, GetBoolDefault("default_auto_update_enabled", false)).
		"default_auto_update_enabled": "false",
```

(This is a one-line addition to the existing `want` map literal, the
surrounding lines shown above are for placement context, not a full
replacement of the function.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/... -run TestSettingDefaultsMatchConsumers -v`
Expected: FAIL: `settingDefaults["default_auto_update_enabled"] = "", want "false"` (the key doesn't exist in `settingDefaults` yet).

- [ ] **Step 3: Write minimal implementation**

In `internal/httpapi/settings.go`, add to `settingDefaults` (after
`"auto_remove_gone": "true",` or anywhere in the map, Go map literals are
unordered, but for readability add it near the other auto-update-related
keys):

```go
	"default_auto_update_enabled": "false",
```

Add to `settingKeys`:

```go
	{"default_auto_update_enabled", false},
```

Do NOT add `"default_auto_update_enabled"` to `restartRequired`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/httpapi/... -run TestSettingDefaultsMatchConsumers -v`
Expected: PASS

- [ ] **Step 5: Run the full httpapi suite to confirm no regression**

Run: `go test ./internal/httpapi/... 2>&1 | tail -20`
Expected: all tests pass. In particular, any test that asserts the full set
of keys returned by `GET /api/settings` (grep the file for `handleGetSettings`
tests) may need its expected key list updated to include
`default_auto_update_enabled`: if such a test fails, add the key to its
expected set; do not weaken the assertion.

- [ ] **Step 6: Build, vet, full suite**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, vet clean, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/settings.go internal/httpapi/settings_test.go
git commit -m "$(cat <<'EOF'
feat(settings): expose default_auto_update_enabled via settings API

Whitelisted as a normal hot-reloadable setting (not restart-required),
since discovery already re-reads it live every reconcile pass.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Frontend: settings type, toggle, tests

**Files:**
- Modify: `web/src/api/types.ts` (the `Settings` interface)
- Modify: `web/src/components/settings/UpdatesSettings.tsx`
- Modify: `web/src/components/settings/UpdatesSettings.test.tsx`

**Interfaces:**
- Consumes: the exact setting key `"default_auto_update_enabled"` from Task 3 (must match the Go-side string byte-for-byte, since `SettingKey = keyof Settings` and the API round-trips this key as a plain JSON string field).
- Produces: nothing consumed by later tasks (this is the last task).

- [ ] **Step 1: Add the field to the `Settings` interface**

In `web/src/api/types.ts`, in the `Settings` interface, add a line (placement
doesn't matter (TypeScript interfaces are unordered) but add it near
`auto_remove_gone` for readability):

```ts
  default_auto_update_enabled: string;
```

So the interface reads:

```ts
export interface Settings {
  poll_interval_seconds: string;
  scan_on_start: string;
  concurrency: string;
  health_timeout_seconds: string;
  health_poll_seconds: string;
  cache_ttl_seconds: string;
  write_back_compose: string;
  auto_remove_gone: string;
  default_auto_update_enabled: string;
  gone_grace_seconds: string;
  job_retention_days: string;
  github_token_set: boolean;
  restart_required: string[];
  /** Server-side default for each non-secret setting; lets the UI dim untouched fields. */
  defaults: Record<string, string>;
}
```

- [ ] **Step 2: Update the test fixture (required for the suite to typecheck)**

In `web/src/components/settings/UpdatesSettings.test.tsx`, the `SETTINGS`
object is typed as `Settings`, so it will fail to typecheck once the
interface gains a required field. Add the field to the fixture:

```ts
const SETTINGS: Settings = {
  poll_interval_seconds: "900",
  scan_on_start: "true",
  concurrency: "4",
  health_timeout_seconds: "60",
  health_poll_seconds: "3",
  cache_ttl_seconds: "300",
  write_back_compose: "false",
  auto_remove_gone: "false",
  default_auto_update_enabled: "false",
  gone_grace_seconds: "86400",
  job_retention_days: "30",
  github_token_set: false,
  restart_required: [],
  // gone_grace_seconds sits on its server-side default so the "default" hint
  // has a populated case to dim.
  defaults: { poll_interval_seconds: "900", gone_grace_seconds: "86400" },
};
```

- [ ] **Step 3: Write the failing test**

Add to `web/src/components/settings/UpdatesSettings.test.tsx`, inside the
`describe("UpdatesSettings", ...)` block (after the `"write-back switch
toggles..."` test):

```tsx
  it("default-auto-update switch toggles and marks the form dirty", async () => {
    const user = userEvent.setup();
    renderPage();
    const sw = await screen.findByRole("switch", { name: /auto-update newly discovered/i });
    expect(sw).toHaveAttribute("data-state", "unchecked");
    await user.click(sw);
    expect(sw).toHaveAttribute("data-state", "checked");
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
  });

  it("saves default_auto_update_enabled as a string boolean", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByLabelText(/auto-update newly discovered/i));
    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ default_auto_update_enabled: "true" });
    });
  });
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd web && npx vitest run UpdatesSettings`
Expected: FAIL: no element found with accessible name matching
`/auto-update newly discovered/i` (the toggle doesn't exist in the component
yet).

- [ ] **Step 5: Write minimal implementation**

In `web/src/components/settings/UpdatesSettings.tsx`, add
`"default_auto_update_enabled"` to the `KEYS` array:

```tsx
const KEYS: SettingKey[] = [
  "health_timeout_seconds",
  "health_poll_seconds",
  "write_back_compose",
  "auto_remove_gone",
  "default_auto_update_enabled",
  "gone_grace_seconds",
  "job_retention_days",
];
```

Add a new toggle block right after the `auto_remove_gone` toggle block (i.e.
right before the `<div className="flex items-center gap-3">` Save-button
block):

```tsx
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="default_auto_update_enabled">Auto-update newly discovered projects</Label>
            <HelpTooltip text="New compose stacks found on this host start with auto-update off, unless you turn this on. Only affects stacks discovered from now on, existing projects are never touched." />
            {isDefault("default_auto_update_enabled") && <DefaultHint />}
          </div>
          <Switch
            id="default_auto_update_enabled"
            checked={form.default_auto_update_enabled === "true"}
            onCheckedChange={(checked) => setField("default_auto_update_enabled", checked ? "true" : "false")}
          />
        </div>
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd web && npx vitest run UpdatesSettings`
Expected: all tests in the file PASS, including the two new ones.

- [ ] **Step 7: Typecheck, build, full web suite**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: no type errors, build succeeds.

Run: `cd web && npm test`
Expected: all tests pass (no other file references the `Settings` interface
in a way that would break from this additive field, a required-field
addition to `Settings` only breaks fixtures that build a full `Settings`
object literal, which is only `UpdatesSettings.test.tsx`; if `tsc` surfaces
another file, add the field there too and re-run).

- [ ] **Step 8: Commit**

```bash
git add web/src/api/types.ts web/src/components/settings/UpdatesSettings.tsx web/src/components/settings/UpdatesSettings.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): add toggle for default auto-update on new projects

Same pattern as the existing auto-remove-gone switch. Off by default,
matching the backend's shipped default.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```
