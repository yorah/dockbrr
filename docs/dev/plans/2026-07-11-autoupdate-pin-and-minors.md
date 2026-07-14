# Auto-Update Pin Fix + Backlog Minors, Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make auto-update veto only on a *genuine* (user-declared) digest pin, not a dockbrr fallback runtime-pin; plus a cluster of non-blocking backlog minors from the write-back and dashboard-lifecycle whole-branch reviews.

**Architecture:** One behavior change in `store.EffectiveAutoUpdate` (reuse the existing `Drifted` signal to distinguish a fallback pin from a real pin), then small hardening/test/tidy items. Backlog is updated to reflect what's now resolved.

**Tech Stack:** Go 1.26 (CGO-free), React + TS + vitest.

## Global Constraints

- CGO_ENABLED=0 stays green: `CGO_ENABLED=0 go build ./...`; `go vet ./... && go test ./...`.
- TS: `./node_modules/.bin/tsc -b --noEmit` then `npm run build` (NOT `npx tsc`); after any web build `git checkout -- internal/httpapi/dist/index.html`; commit only `web/src/` sources.
- No new schema (reuse `services.Drifted`, added earlier).

---

### Task 1: EffectiveAutoUpdate vetoes only genuine pins (not fallback pins)

**Files:**
- Modify: `internal/store/projects.go` (`EffectiveAutoUpdate`)
- Test: `internal/store/projects_test.go`
- Modify: `docs/dev/backlog.md` (update the Pinned-after-apply entry)

**Rationale:** `Pinned` = the running container ref contains `@sha256:`. That is true both when the USER pinned a digest in their compose file AND when dockbrr's fallback runtime-override pinned it during an apply of a `${VAR}`/non-rewritable service. The two are distinguished by `Drifted`: a real user pin has running==declared (not drifted); a fallback pin has the file still declaring a tag while the container runs a digest (drifted). So veto auto-update only when `Pinned && !Drifted`.

**Interfaces:** consumes `store.Service.Drifted` (existing bool).

- [ ] **Step 1: Write the failing tests**

```go
func TestEffectiveAutoUpdateGenuinePinVetoes(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	// User pinned a digest in the file: pinned, NOT drifted -> vetoed.
	s := store.Service{Pinned: true, Drifted: false}
	if store.EffectiveAutoUpdate(p, s) {
		t.Error("genuine digest pin (pinned && !drifted) must veto auto-update")
	}
}

func TestEffectiveAutoUpdateFallbackPinAllows(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	// Fallback runtime-pin (file still a tag): pinned AND drifted -> allowed.
	s := store.Service{Pinned: true, Drifted: true}
	if !store.EffectiveAutoUpdate(p, s) {
		t.Error("fallback pin (pinned && drifted) must NOT veto auto-update")
	}
}

func TestEffectiveAutoUpdateUnpinnedAllows(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	if !store.EffectiveAutoUpdate(p, store.Service{Pinned: false, Drifted: false}) {
		t.Error("unpinned service must allow auto-update")
	}
}

func TestEffectiveAutoUpdateProjectOffVetoes(t *testing.T) {
	if store.EffectiveAutoUpdate(store.Project{AutoUpdateEnabled: false}, store.Service{}) {
		t.Error("project auto-update off must veto")
	}
}

func TestEffectiveAutoUpdateServiceVetoOverride(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	no := false
	if store.EffectiveAutoUpdate(p, store.Service{AutoUpdateEnabled: &no}) {
		t.Error("explicit per-service false must veto")
	}
}
```
(Match the real `store.Project`/`store.Service` field names: read the structs first; `AutoUpdateEnabled` on Project is a bool, on Service a `*bool`.)

- [ ] **Step 2: Run red**: `go test ./internal/store/ -run TestEffectiveAutoUpdate` → the fallback-pin test FAILs (currently vetoed).

- [ ] **Step 3: Implement**

```go
// EffectiveAutoUpdate is the auto-apply gate: the project flag must be on, the
// service must not veto it, and the service must not be GENUINELY digest-pinned.
// A pin counts as genuine only when the running digest matches the compose
// file (not Drifted): a user who pinned a digest in their file is left alone,
// but a service dockbrr fallback-pinned at runtime (its file still tracks a
// tag -> Drifted) stays auto-updatable so it keeps following its tag.
func EffectiveAutoUpdate(p Project, s Service) bool {
	if !p.AutoUpdateEnabled || (s.Pinned && !s.Drifted) {
		return false
	}
	return s.AutoUpdateEnabled == nil || *s.AutoUpdateEnabled
}
```

- [ ] **Step 4: Run green**: `go test ./internal/store/` → PASS. `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Update backlog**: in `docs/dev/backlog.md`, edit the `[smoke-2026-07-10] Pinned-after-apply UX gotcha` entry: note it is now largely RESOLVED (write-back keeps exact/floating tags un-pinned; `EffectiveAutoUpdate` no longer vetoes a fallback pin via `pinned && !drifted`), leaving only a UI-tooltip nicety for a genuinely-pinned service. Also strike `[writeback T4]` as moot (the GET special-case was collapsed into `settingDefaults` in the dashboard-lifecycle batch).

- [ ] **Step 6: Commit**
```bash
git add internal/store/projects.go internal/store/projects_test.go docs/dev/backlog.md
git commit -m "fix(store): auto-update vetoes only genuine pins, not fallback pins"
```

---

### Task 2: Discovery/settings hardening (dashux T4a, T4b, T1)

**Files:**
- Modify: `internal/discovery/discovery.go` (grace clamp + Source-gate service delete)
- Modify: `internal/httpapi/settings_test.go` (extend guard test)
- Test: `internal/discovery/discovery_test.go`

- [ ] **Step 1: Write failing tests**
  - **T4a (grace clamp):** a NEGATIVE `gone_grace_seconds` must NOT delete a just-gone service (a negative grace would push the cutoff into the future and delete everything gone). Test: set `gone_grace_seconds=-100`, a service gone "now" (recent `gone_since`) survives Reconcile.
  - **T4b (Source-gate):** already inert, but add a defense-in-depth test. A service with `state="gone"` + old `gone_since` inside a MANUAL project is NOT deleted (construct it by directly Exec-ing state/gone_since on a manual-project service, then Reconcile). It survives because the delete loop is now Source-gated.
  - **T1 (guard test):** extend `TestSettingDefaultsMatchConsumers` to also pin `auto_remove_gone`=="true" and `gone_grace_seconds`=="3600" (the discovery-side consumer defaults), so drift between `settingDefaults` and the `discovery.go` literals is caught.

- [ ] **Step 2: Run red** → FAIL.

- [ ] **Step 3: Implement**
  - In `discovery.go` prune pass: after reading the grace, clamp negatives:
    ```go
    graceSecs := settingIntDefault(r.settings, "gone_grace_seconds", 3600)
    if graceSecs < 0 {
        graceSecs = 0
    }
    grace := time.Duration(graceSecs) * time.Second
    ```
    (Replace the existing inline `time.Duration(settingIntDefault(...)) * time.Second`.)
  - Source-gate the service-delete: move the gone-service delete loop so it runs only for `p.Source == "discovered"` projects (the same gate already used for project deletion). Concretely, wrap the per-service delete loop in `if p.Source == "discovered" { ... }`, or add `&& p.Source == "discovered"`, but note the project List already gives `p.Source`; keep the empty-project deletion inside the same discovered gate. Preserve behavior for discovered projects exactly (tests T4/prune from the prior batch must stay green).
  - In `settings_test.go`, add the two keys to the `want` map in `TestSettingDefaultsMatchConsumers` (values "true" / "3600").

- [ ] **Step 4: Run green**: `go test ./internal/discovery/ ./internal/httpapi/` → PASS (existing prune tests too). `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/discovery/discovery.go internal/httpapi/settings_test.go internal/discovery/discovery_test.go
git commit -m "harden(discovery): clamp negative grace, source-gate service delete, guard gone defaults"
```

---

### Task 3: Write-back cleanups (writeback T3, T6)

**Files:**
- Modify: `internal/job/worker.go` (narrow `rollbackPullUp` signature)
- Test: `internal/discovery/discovery_test.go` (odd-name drift test)

- [ ] **Step 1: Write the failing test (T3 odd-name drift)**. A discovered compose project with a service named with an underscore/dash (e.g. `web_app-1`) whose running ref diverges from the declared image → `Drifted == true`; and a matching non-drift case. This pins that `compose.Parse`'s service name matches the `com.docker.compose.service` label 1:1 for odd names. Match the existing drift-test harness in `discovery_test.go`.

- [ ] **Step 2: Run red** (the drift test) → PASS or FAIL depending on current behavior; if it already passes, that's fine. It's a regression pin (note in the report that it documents existing-correct behavior). The signature-narrow (T6) has no behavior test; it's a compile-verified refactor.

- [ ] **Step 3: Implement T6**: `rollbackPullUp` currently takes `snap store.Snapshot` but reads only `snap.PrevDigest`. Change the signature to take `prevDigest string` instead, update the function body (`snap.PrevDigest` → `prevDigest`), and update both call sites (`worker.go:434`, `worker.go:450`) to pass `snap.PrevDigest`. Pure mechanical narrowing; no behavior change.

- [ ] **Step 4: Run green**: `go test ./internal/job/ ./internal/discovery/` → PASS. `CGO_ENABLED=0 go build ./...`; `go vet ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/job/worker.go internal/discovery/discovery_test.go
git commit -m "cleanup: narrow rollbackPullUp signature; pin odd-name drift"
```

---

### Task 4: History changelog single-row tidy (dashux T10)

**Files:**
- Modify: `web/src/components/HistoryTimeline.tsx`
- Test: `web/src/components/HistoryTimeline.test.tsx`

**Current:** a changelog-bearing entry renders as two sibling `<li>` rows (the `EventItem` row + a separate `EventChangelog` `<li>`). Tidy so the changelog affordance renders INSIDE / attached to the event's own row rather than as a second icon-less `<li>`.

- [ ] **Step 1: Adjust the test**: assert the changelog affordance is associated with its event (e.g. the "Changelog" control is within the same list item / the timeline renders ONE `<li>` per event, not two). Keep the existing behavior assertions (affordance shows when changelog present, hidden when absent, XSS stripped).

- [ ] **Step 2: Run red**: `cd web && npm test -- HistoryTimeline` → FAIL on the single-row assertion.

- [ ] **Step 3: Implement**: render the `EventChangelog` disclosure inside the event's `<li>` (pass the changelog into `EventItem`, or render the affordance within the same list item wrapper) instead of a sibling `<li>` via `Fragment`. Keep it minimal; do not restructure `EventItem`'s existing markup beyond adding the changelog affordance slot. Preserve the sanitized `Changelog` render path (no `dangerouslySetInnerHTML`).

- [ ] **Step 4: Run green**: `npm test -- HistoryTimeline` → PASS (incl. XSS). `./node_modules/.bin/tsc -b --noEmit && npm run build`; `git checkout -- internal/httpapi/dist/index.html`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/HistoryTimeline.tsx web/src/components/HistoryTimeline.test.tsx
git commit -m "fix(web): render history changelog within its event row"
```

---

## Final verification (after all tasks)

- `CGO_ENABLED=0 go build ./...`; `go vet ./... && go test ./...`, green.
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm test && npm run build`, green.
- `git checkout -- internal/httpapi/dist/index.html`.
- Backlog: `[writeback T4]` struck (moot), Pinned-after-apply entry updated, and the four `dashux`/`writeback` minors this plan closes marked done.
