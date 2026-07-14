# Boot Scan Auto-Apply Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `scan_on_start`'s boot scan also auto-apply eligible updates, so a
project that already has auto-update enabled doesn't wait out a full poll interval
after a restart before its update is applied.

**Architecture:** `schedulerLoop`'s boot branch in `cmd/dockbrr/main.go` gains one
call (`autoApply(services, projects, updates, engine)`) right after `runCheck`,
mirroring the existing ticker branch exactly. No new setting, no new gating logic:
`autoApply` already reads `store.EffectiveAutoUpdate` (project `auto_update_enabled`
+ per-service override), so behavior only changes for projects a user has already
opted into auto-update.

**Tech Stack:** Go (`cmd/dockbrr/main.go`), React/TS (`web/src/components/settings/ScanningSettings.tsx`).

## Global Constraints

- No new setting/toggle: reuse `scan_on_start` exactly as-is (spec decision).
- Gate stays exactly `store.EffectiveAutoUpdate`: do not add any new condition.
- `go build ./...`, `go vet ./...`, `go test ./...` must stay green after the Go change.
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build` and `npm test` must stay green after the copy change. Use `tsc` directly, NOT `npx tsc` (rtk hook falsely reports "No errors" for `npx tsc`).
- No new test infra for `schedulerLoop`/`autoApply`. This stays at the same (smoke/manual) coverage level as the existing ticker branch, per approved spec.

---

### Task 1: Boot scan auto-applies eligible updates

**Files:**
- Modify: `cmd/dockbrr/main.go:486-494` (boot branch of `schedulerLoop`)

**Interfaces:**
- Consumes: existing `autoApply(services *store.Services, projects *store.Projects, updates *store.Updates, engine *job.Engine)` (defined at `cmd/dockbrr/main.go:587`, unchanged signature): same function the ticker branch already calls at line ~503 (`autoApply(services, projects, updates, engine)`).
- Produces: nothing new consumed by later tasks. This is the sole backend change.

- [ ] **Step 1: Confirm current boot-branch code**

Read `cmd/dockbrr/main.go` lines 486-494. It currently reads:

```go
	// The boot scan is detection-only: a restart must not itself trigger Docker
	// mutation, so auto-apply stays on the ticker where the operator put it.
	// It also does not reset the ticker. The next tick stays one full interval
	// from boot, just with fresh data on screen in the meantime.
	if settings.GetBoolDefault("scan_on_start", true) {
		waitForDiscovery(ctx, discoveryReady, discoveryReadyTimeout)
		logger.Infof("scheduler: running startup check (scan_on_start)")
		runCheck(ctx, settings, scanner, bus)
	}
```

- [ ] **Step 2: Replace the block**

Replace the exact block from Step 1 with:

```go
	// The boot scan also auto-applies: any project/service that already has
	// auto-update enabled (store.EffectiveAutoUpdate) gets its eligible update
	// applied immediately rather than waiting up to one poll interval, the
	// same gate the ticker uses, so this only changes behavior for projects a
	// user already opted into auto-update. It does not reset the ticker, the
	// next tick stays one full interval from boot, just with fresh data (and,
	// where configured, a fresh apply) in the meantime.
	if settings.GetBoolDefault("scan_on_start", true) {
		waitForDiscovery(ctx, discoveryReady, discoveryReadyTimeout)
		logger.Infof("scheduler: running startup check (scan_on_start)")
		runCheck(ctx, settings, scanner, bus)
		autoApply(services, projects, updates, engine)
	}
```

- [ ] **Step 3: Build, vet, test**

Run:
```bash
CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...
```
Expected: build succeeds, vet reports no issues, all existing tests pass (this task adds no new Go tests per the Global Constraints: `autoApply` and `schedulerLoop` have no existing unit harness to extend).

- [ ] **Step 4: Commit**

```bash
git add cmd/dockbrr/main.go
git commit -m "$(cat <<'EOF'
feat(scheduler): auto-apply eligible updates on boot scan

scan_on_start now calls autoApply right after runCheck, same as the
ticker branch. Gate is unchanged (store.EffectiveAutoUpdate), so this
only affects projects/services that already have auto-update enabled.
They no longer wait out a poll interval after a restart to pick up
an update that was already eligible.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Update settings UI copy to match new behavior

**Files:**
- Modify: `web/src/components/settings/ScanningSettings.tsx:58`

**Interfaces:**
- Consumes: nothing from Task 1 (pure copy change, independent of the Go build).
- Produces: nothing consumed elsewhere.

- [ ] **Step 1: Confirm current tooltip text**

Read `web/src/components/settings/ScanningSettings.tsx` line 58. It currently reads:

```tsx
            <HelpTooltip text="Run one scan as soon as dockbrr starts, instead of waiting a full poll interval for the first one. Detection only, auto-update still applies on the poll interval, so a restart never applies an update by itself." />
```

- [ ] **Step 2: Replace the tooltip text**

Replace that exact line with:

```tsx
            <HelpTooltip text="Run one scan as soon as dockbrr starts, instead of waiting a full poll interval for the first one. If a project has auto-update on, eligible updates found at boot are applied immediately too, same as any scheduled scan." />
```

- [ ] **Step 3: Typecheck, build, test**

Run:
```bash
cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build
```
Expected: no type errors, build succeeds.

Run:
```bash
cd web && npm test -- ScanningSettings
```
Expected: all tests in `ScanningSettings.test.tsx` pass (no test asserts the old tooltip string, verified during planning by grepping `ScanningSettings.test.tsx` for the old copy, no match).

- [ ] **Step 4: Commit**

```bash
git add web/src/components/settings/ScanningSettings.tsx
git commit -m "$(cat <<'EOF'
docs(web): fix scan-on-launch tooltip after boot auto-apply change

The tooltip claimed a restart never applies an update by itself; that
stopped being true once scan_on_start started calling autoApply.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```
