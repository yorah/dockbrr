# Settings revamp: left sub-nav, card pages, Application info, Add-project dialog

Date: 2026-07-13
Status: approved (brainstorming)

## Problem

`web/src/routes/settings.tsx` is a six-tab shadcn `Tabs` strip. Three problems:

1. **No deep links.** Every section lives at `/settings`; a reload or a shared link always lands on General.
2. **General is a wall.** `GeneralSettings.tsx` (269 lines) holds ten unrelated knobs: scan cadence, health-gate timings, compose write-back, gone-removal, job retention, the GitHub token, and settings export/import.
3. **"Add project" is not a setting.** Creating a project is an action on the workload list, not a preference. Burying it in a settings tab hides the only way to register a compose project dockbrr cannot auto-discover.

There is also no place to see what build is running, whether Docker is reachable, or where the database lives.

## Design

A left sub-nav settings screen: one-concern pages, each page a stack of cards.

### 1. Routing

`/settings` redirects to `/settings/application`. Each section is a nested route under a `SettingsLayout`:

| Route | Page | Content |
|---|---|---|
| `/settings/application` | `ApplicationSettings` | Build · Runtime · Docker · Storage · Authentication · Backup |
| `/settings/scanning` | `ScanningSettings` | poll interval, scan on launch, concurrency, registry cache TTL |
| `/settings/updates` | `UpdatesSettings` | health timeout, health poll interval, write-back to compose, auto-remove gone + grace, job retention |
| `/settings/auto-update` | `AutoUpdateToggles` (unchanged) | per-project / per-service toggles |
| `/settings/registries` | `RegistriesSettings` | registry credentials + GitHub token |
| `/settings/security` | `PasswordSettings` | password change |
| `/settings/logs` | `LogsSettings` (unchanged) | log config + log files |

Routes are declared in `web/src/router.tsx` as children of a `settingsRoute` whose component is `SettingsLayout`. The redirect is a `beforeLoad` on the index child.

### 2. `SettingsLayout`

Left nav (`w-56`) of icon + label rows, reusing `rowClass` / `rowActiveClass` from `components/layout/SidebarNav.tsx` so the active-row treatment matches the app sidebar exactly. Content area renders `<Outlet/>`.

Below `md`, the nav collapses to a horizontally scrollable row above the content. A second vertical sidebar is unusable on a phone.

### 3. Card primitives

Two new components carry the whole look; every settings page is built from them.

- `components/settings/SettingsCard.tsx`: `{ title, description, action?, children }`. Bordered `bg-card` panel; header row with title + one-line description on the left and an optional action slot on the right (e.g. a Refresh button); body below.
- `components/settings/InfoRow.tsx`: `{ label, value, sub? }`. A read-only pair: uppercase muted label on the left, mono value on the right, `border-t` divider between rows, optional muted sub-line beneath the value (e.g. `13d 14h ago` under a build date).

Form pages use `SettingsCard` + the existing `Label`/`Input`/`Switch` fields. Read-only pages use `SettingsCard` + `InfoRow`.

### 4. Backend: `GET /api/system/info`

New `internal/httpapi/system.go`, authenticated like `/api/status`, read-only, touches no Docker mutation (safety invariant 2 holds).

```json
{
  "version": "0.1.0-dev",
  "commit": "b563d4b",
  "commit_dirty": false,
  "build_date": "2026-06-29T21:48:15Z",
  "go_version": "go1.26.4",
  "platform": "linux/amd64",
  "started_at": "2026-07-11T09:14:02Z",
  "docker": { "reachable": true, "version": "27.3.1", "api_version": "1.47" },
  "db_path": "/config/dockbrr.db",
  "bind_addr": "0.0.0.0:3625",
  "data_dir": "/config",
  "auth": { "username": "admin", "method": "password" }
}
```

Sources:

- `version`: `internal/version.Version` (existing const).
- `commit`, `commit_dirty`, `build_date` (`debug.ReadBuildInfo()` VCS stamps (`vcs.revision`, `vcs.modified`, `vcs.time`). Go stamps these automatically when building from a git checkout; no ldflags and no change to `mise.toml`. When absent (`go run`, VCS stamping off) the fields are empty strings / `false` and the UI renders a dash placeholder.
- `go_version`, `platform`: `runtime.Version()`, `runtime.GOOS + "/" + runtime.GOARCH`.
- `started_at`: process start time captured in `main.go` and passed into `Deps`. **Uptime is computed client-side** from `started_at` via the existing `useNow` hook, so the value ticks without polling and no server/client clock-skew field is needed.
- `docker` (reachability from the existing `DockerPinger` probe (2s timeout). Version/API version from a new `Deps.DockerVersion func(ctx) (version, apiVersion string, err error)`) an optional func field, mirroring the existing `NextScan func() time.Time` idiom, so tests can leave it nil. It is backed by a new `(*docker.Client).ServerVersion` method wrapping the SDK's `ServerVersion`. When Docker is unreachable, `reachable` is `false` and the version fields are omitted.
- `db_path`, `bind_addr`, `data_dir`: from the bootstrap `config.Config` already held on `Server` (`filepath.Join(cfg.DataDir, "dockbrr.db")`, `cfg.BindAddr`, `cfg.DataDir`).
- `auth.username`: the current session's user, looked up from the auth context. `auth.method` is the constant `"password"` (single-user password auth is the only mode).

No self-update / "latest release" check: it would require an outbound GitHub call on a page load. Can be added later.

**Not exposed:** no secrets, no token values, no password hash, no Docker socket contents. `db_path` and `bind_addr` are operator-facing config the authenticated single user already controls.

### 5. Frontend component splits

- `GeneralSettings.tsx` splits into `ScanningSettings.tsx` and `UpdatesSettings.tsx`. Both need the same dirty-tracking / save-diff / `DefaultHint` behavior, so that logic is extracted once into `hooks/useSettingsForm.ts`: takes the list of editable keys, returns `{ form, setField, isDefault, dirty, save }` and sends only changed keys on save. The two pages become field lists.
- The GitHub token field moves to `RegistriesSettings.tsx` as its own card. **Write-only invariant preserved:** placeholder shows `Set` / `Not set`, the value is never echoed, and `github_token` is included in the PUT only when a new value was typed.
- Export / Import settings move to `ApplicationSettings` as a **Backup** card, app-level, not a scanning knob.
- `ManualProject.tsx` becomes `components/AddProjectDialog.tsx`: the same form, wrapped in the existing `Dialog` primitive, with `open` / `onOpenChange` props. It is removed from settings entirely.

### 6. Add project

One shared `AddProjectDialog`, three triggers:

1. A `+` icon-button on the sidebar's **Projects** section header (`aria-label="Add project"`): permanent home, reachable from every route.
2. An `+ Add project` button in the dashboard action row, beside Check all / Apply all, where people look first.
3. The dashboard empty state ("No workloads discovered…") gains an **Add project** CTA, which is the case where it matters most.

On success: toast, `invalidateQueries(keys.projects)`, dialog closes.

## Testing

- `useSettingsForm`: dirty detection, default-hint, save sends only changed keys.
- `ApplicationSettings` (renders info rows from a mocked `/api/system/info`; renders the dash placeholder when `commit` / `build_date` are empty; uptime derives from `started_at`.
- `RegistriesSettings`: GitHub token stays write-only (never prefilled; omitted from PUT when untouched).
- `ScanningSettings` / `UpdatesSettings`: inherit the split assertions from the existing `GeneralSettings.test.tsx`.
- Settings routing: `/settings` redirects to `/settings/application`; each sub-route renders its page; nav marks the active row.
- `AddProjectDialog`: submit creates the project and closes; rendered from both sidebar and dashboard triggers.
- Go: `TestSystemInfo`: authenticated 200 with version/platform/db_path; unauthenticated 401; nil `DockerVersion` and unreachable Docker degrade to `reachable:false` without erroring; missing VCS stamps yield empty strings, not a 500.

## Out of scope

- Self-update / latest-release check.
- Any change to the discovery pipeline, job engine, or compose command building.
- Reworking `AutoUpdateToggles` or `LogsSettings` beyond wrapping them in `SettingsCard`.
