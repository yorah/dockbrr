# Default auto-update for newly discovered projects

## Problem

Every project auto-update starts off, and there is no way to change that. A
newly discovered compose stack always lands with `auto_update_enabled=0`
(the schema's hardcoded default), forcing a manual opt-in per project even
for users who want every new stack picked up automatically.

## Design

Add a global setting, `default_auto_update_enabled` (default `"false"`, no
behavior change out of the box), that governs `auto_update_enabled` for
projects discovery inserts for the first time. Existing projects are never
touched.

**Store.** `internal/store/projects.go` `Upsert`'s INSERT currently omits the
`auto_update_enabled` column, relying on the schema's `DEFAULT 0`. Add the
column, sourced from `pr.AutoUpdateEnabled` (the caller decides the value).
The `ON CONFLICT` clause already excludes this column, unchanged, so
re-upserting an existing project never resets it.

**Settings whitelist.** `internal/httpapi/settings.go`: add
`"default_auto_update_enabled": "false"` to `settingDefaults`, add
`{"default_auto_update_enabled", false}` to `settingKeys`. Not added to
`restartRequired`: discovery already re-reads settings live every reconcile
pass (same as `auto_remove_gone`), so this is hot-reloadable.

**Discovery wiring.** `internal/discovery/discovery.go:248-256`, the branch
that upserts a brand-new discovered project (not the `isManual` reuse
branch: manual projects are never re-upserted through this path), sets:

```go
AutoUpdateEnabled: r.settings != nil && r.settings.GetBoolDefault("default_auto_update_enabled", false),
```

mirroring the existing nil-safe settings read already used for
`auto_remove_gone` (discovery.go:352).

**Scope: discovery only.** Manual "Add project"
(`handleCreateProject`, `internal/httpapi/settings.go:393`) goes through the
same `Upsert` but never sets `AutoUpdateEnabled` on the `store.Project{}` it
builds, so it keeps inserting `false` regardless of this setting.
Manually-added projects keep today's explicit-opt-in-only behavior, this
setting only changes what discovery does with stacks it finds on its own.

**UI.** `web/src/components/settings/UpdatesSettings.tsx`, same
toggle/tooltip pattern as `auto_remove_gone`:

- Label: "Auto-update newly discovered projects"
- Tooltip: "New compose stacks found on this host start with auto-update
  off, unless you turn this on. Only affects stacks discovered from now on;
  existing projects are never touched."

Add `default_auto_update_enabled: string;` to the `Settings` interface in
`web/src/api/types.ts`, and the key to `UpdatesSettings.tsx`'s `KEYS` array.

## Testing

- `internal/store/projects_test.go`: new test: `Upsert(store.Project{...,
  AutoUpdateEnabled: true})` on a project with no existing (host_id, name)
  row persists `auto_update_enabled=1`. The existing
  `TestProjectsUpsertByNaturalKeyPreservesUserColumns` test (the
  conflict-preserve case) must keep passing unmodified, it locks in that the
  `ON CONFLICT` path never resets a user's existing value.
- `internal/discovery/discovery_test.go`: new test: with
  `default_auto_update_enabled` set to `"true"`, `Reconcile` on a brand-new
  project persists `AutoUpdateEnabled: true`. Existing reconcile tests (which
  never set this key) continue to assert `false`, pinning no regression to
  the shipped default.
- `web/src/components/settings/UpdatesSettings.test.tsx`: extend for the new
  toggle, same pattern as the existing `auto_remove_gone` toggle test.
- `go build`/`go vet`/`go test ./...` and the web
  `tsc -b --noEmit`/`npm run build`/`npm test` must stay green.
