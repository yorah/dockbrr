# Boot scan auto-applies configured updates

## Problem

`scan_on_start` runs one detection pass at boot so the dashboard doesn't show stale
data for a whole poll interval, but it never auto-applies, even for
projects/services that already have auto-update turned on. Those eligible updates
sit undetected-but-unactioned until the first scheduled tick (up to
`poll_interval_seconds`, 15 min default). A user who restarts dockbrr right after
an update lands has to wait out that window, or apply manually, even though the
project is configured to auto-apply.

## Design

`schedulerLoop`'s boot branch (`cmd/dockbrr/main.go`) calls `autoApply(services,
projects, updates, engine)` immediately after `runCheck(...)`, exactly mirroring
what the ticker branch already does on every scheduled tick:

```go
if settings.GetBoolDefault("scan_on_start", true) {
	waitForDiscovery(ctx, discoveryReady, discoveryReadyTimeout)
	logger.Infof("scheduler: running startup check (scan_on_start)")
	runCheck(ctx, settings, scanner, bus)
	autoApply(services, projects, updates, engine)
}
```

No new setting. `autoApply` gates purely on the existing `store.EffectiveAutoUpdate`
check: project `auto_update_enabled` (schema default `0`/off) plus the per-service
override: the same gate the ticker already uses. A restart only mutates Docker for
projects a user has already explicitly opted into auto-update; everyone else sees
identical behavior to today.

**Why this is safe to fold into `scan_on_start` itself, not a separate toggle:**
the two knobs would always be set together in practice (auto-apply-on-boot is
meaningless without also scanning on boot to discover the drift), and the real
safety boundary is per-project `auto_update_enabled`, not `scan_on_start`. Adding
a second switch would add UI surface without adding real protection.

**Ordering/safety.** Boot auto-apply runs after `waitForDiscovery`'s bounded wait
(shipped in the previous fix), so it acts on freshly-reconciled service rows, not
pre-recreate ones: the same protection that fix gave detection now covers
mutation too, which matters more. The job engine's per-project keyed mutex already
serializes any overlap with `ResumeInterrupted`'s requeued jobs at boot; no new
synchronization is needed.

**Docs/copy to update:**
- `cmd/dockbrr/main.go`: the boot-scan comment currently says "a restart must not
  itself trigger Docker mutation": rewrite to describe the new behavior and why
  it's still bounded (same gate as the ticker, opt-in only).
- `web/src/components/settings/ScanningSettings.tsx:58`: the "Scan on launch"
  tooltip currently claims "a restart never applies an update by itself", this
  becomes false and must be rewritten, e.g.: "Run one scan as soon as dockbrr
  starts, instead of waiting a full poll interval for the first one. If a
  project has auto-update on, eligible updates found at boot are applied
  immediately too: same as any scheduled scan."

## Testing

No existing unit test exercises `schedulerLoop`'s ticker branch or `autoApply` in
isolation (covered only by smoke/manual testing today); the boot branch stays at
that same coverage level for symmetry. No new test infra introduced for this.
`go build`/`go vet`/`go test ./...` and the web typecheck/build must stay green
after the copy changes.
