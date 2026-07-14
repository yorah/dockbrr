# Recreate Re-Surfaces Updates: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Implement `docs/dev/specs/2026-07-11-recreate-resurfaces-updates-design.md`: a `rolled_back` status so RecordDrift can re-open `applied` (recreate re-surfaces the update) while preserving a real rollback, plus detect-cache invalidation on a running-digest change.

**Architecture:** Store change (RecordDrift re-opens `applied`; new `MarkRolledBack`; dashboard list includes `rolled_back`) → Job change (`runRollback` marks `rolled_back`) → Discovery cache invalidation → Frontend rolled-back badge + restore.

**Tech Stack:** Go 1.26 (CGO-free), SQLite, React + TS + vitest.

## Global Constraints

- CGO_ENABLED=0 stays green; `go vet ./... && go test ./...`.
- All Docker mutation via the Job Engine; discovery/detect stay read-only except the store writes they own. Cache invalidation is a store DELETE.
- TS: `./node_modules/.bin/tsc -b --noEmit` then `npm run build` (NOT `npx tsc`); after any web build `git checkout -- internal/httpapi/dist/index.html`; commit only `web/src/` sources.
- Status vocabulary: `available | dismissed | applied | failed | superseded | rolled_back`.
- Auto-update / apply act ONLY on `available`: `rolled_back` must never be auto-applied.

---

### Task 1: Store: RecordDrift re-opens `applied`, `MarkRolledBack`, dashboard list includes `rolled_back`

**Files:**
- Modify: `internal/store/updates.go`
- Test: `internal/store/updates_test.go`

**Interfaces produced (consumed by later tasks):**
- `Updates.MarkRolledBack(serviceID int64, toDigest string) error`
- The dashboard list method returns `available` + `dismissed` + `rolled_back`.

- [ ] **Step 1: Write failing tests**

```go
func TestRecordDriftReopensApplied(t *testing.T) {
	db := openTempStore(t) // reuse this file's real harness
	svcID := seedServiceForUpdates(t, db) // reuse existing helper/pattern
	u := store.NewUpdates(db)
	// Applied update at digest A.
	id, _, err := u.RecordDrift(store.Update{ServiceID: svcID, FromDigest: "old", ToDigest: "A", Status: "applied"})
	if err != nil { t.Fatal(err) }
	_ = id
	// Re-detect the SAME target (service diverged back, e.g. recreate): must re-open to available.
	if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, FromDigest: "current", ToDigest: "A"}); err != nil {
		t.Fatal(err)
	}
	got, _ := u.Get(id)
	if got.Status != "available" {
		t.Fatalf("applied re-detect status = %q, want available (recreate must re-surface)", got.Status)
	}
}

func TestRecordDriftPreservesRolledBackAndDismissed(t *testing.T) {
	db := openTempStore(t)
	svcID := seedServiceForUpdates(t, db)
	u := store.NewUpdates(db)
	for _, status := range []string{"rolled_back", "dismissed"} {
		id, _, _ := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "D-" + status, Status: status})
		if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "D-" + status}); err != nil {
			t.Fatal(err)
		}
		got, _ := u.Get(id)
		if got.Status != status {
			t.Fatalf("%s must be preserved on re-detect, got %q", status, got.Status)
		}
	}
}

func TestMarkRolledBack(t *testing.T) {
	db := openTempStore(t)
	svcID := seedServiceForUpdates(t, db)
	u := store.NewUpdates(db)
	id, _, _ := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "A", Status: "applied"})
	if err := u.MarkRolledBack(svcID, "A"); err != nil { t.Fatal(err) }
	got, _ := u.Get(id)
	if got.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back", got.Status)
	}
	// Only touches an APPLIED row: a second call on an available row is a no-op.
	id2, _, _ := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "B", Status: "available"})
	_ = u.MarkRolledBack(svcID, "B")
	got2, _ := u.Get(id2)
	if got2.Status != "available" {
		t.Fatalf("MarkRolledBack must only affect applied rows, got %q", got2.Status)
	}
}

func TestListVisibleIncludesRolledBack(t *testing.T) {
	db := openTempStore(t)
	svcID := seedServiceForUpdates(t, db)
	u := store.NewUpdates(db)
	// Insert `available` LAST: RecordDrift's supersede tail demotes any earlier
	// available row with a different to_digest, so a first-inserted available
	// would be flipped to superseded by the later inserts.
	u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "di", Status: "dismissed"})
	u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "rb", Status: "rolled_back"})
	u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "ap", Status: "applied"})
	u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "av", Status: "available"})
	list, err := u.ListVisible()
	if err != nil { t.Fatal(err) }
	statuses := map[string]bool{}
	for _, up := range list { statuses[up.Status] = true }
	if !statuses["available"] || !statuses["dismissed"] || !statuses["rolled_back"] {
		t.Fatalf("ListVisible must include available+dismissed+rolled_back, got %v", statuses)
	}
	if statuses["applied"] {
		t.Fatalf("ListVisible must NOT include applied")
	}
}
```
(Use the real seeding helpers/`NewUpdates`/`Get` names from the package (read `updates_test.go` first. The `RecordDrift` supersede tail demotes `available` siblings with a different `to_digest`; the tests above use distinct digests so that tail doesn't interfere, except `TestListVisible` where the last-inserted `available` "av" may get superseded by later inserts) assert on the presence of the three visible STATUSES via the distinct digests, and if the supersede tail flips "av", adjust the test to insert `available` LAST or assert on `rolled_back`+`dismissed` presence + `applied` absence, which are the load-bearing claims.)

- [ ] **Step 2: Run red**: `go test ./internal/store/ -run 'RecordDrift|MarkRolledBack|ListVisible'` → FAIL.

- [ ] **Step 3: Implement**
  - In `RecordDrift`'s existing-row branch, extend the status CASE to re-open `applied` too:
    ```sql
    status = CASE WHEN status IN ('failed','superseded','applied') THEN 'available' ELSE status END
    ```
    (was `('failed','superseded')`). Update the surrounding comment: `applied` is now re-opened on re-detect (a service that diverged from its applied target (recreate) is pending again); `dismissed` and `rolled_back` are preserved (user-intent suppression). Remove the stale "applied preserved for rollback-respect" note.
  - Add `MarkRolledBack`:
    ```go
    // MarkRolledBack flips a service's APPLIED update at toDigest to rolled_back
    // (a user-initiated rollback). Gated on status='applied' so it only demotes
    // the row the rollback actually reverted; rolled_back is preserved by
    // RecordDrift and excluded from the auto-apply path.
    func (u *Updates) MarkRolledBack(serviceID int64, toDigest string) error {
    	_, err := u.db.Exec(
    		`UPDATE updates SET status='rolled_back'
    		   WHERE service_id=? AND to_digest=? AND status='applied'`,
    		serviceID, toDigest)
    	return err
    }
    ```
  - Rename `ListOpenAndDismissed` → `ListVisible`, change its `WHERE status IN ('available','dismissed')` to `('available','dismissed','rolled_back')`, update the doc comment, and update its single caller `internal/httpapi/updates.go:28` (`s.deps.Updates.ListOpenAndDismissed()` → `ListVisible()`). Update any test referencing the old name.

- [ ] **Step 4: Run green**: `go test ./internal/store/ ./internal/httpapi/` → PASS. `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/store/updates.go internal/store/updates_test.go internal/httpapi/updates.go
git commit -m "feat(store): RecordDrift re-opens applied; rolled_back status + ListVisible"
```

---

### Task 2: Job: `runRollback` marks the reverted update `rolled_back`

**Files:**
- Modify: `internal/job/worker.go`
- Test: `internal/job/worker_test.go`

**Interfaces:** consumes `Updates.MarkRolledBack` (Task 1).

**Context:** `runRollback` loads `svc` (its `CurrentDigest` is the pre-rollback
applied-target digest = the `to_digest` of the applied update). The shared
`rollbackPullUp` tail emits the `rolled_back` event and calls `succeed`. Add the
status flip there so BOTH rollback branches (blob-restore + digest-repin) get it.

- [ ] **Step 1: Write failing test**. A rollback job for a service that has an
  `applied` update at the service's current digest ends with that update's status
  `rolled_back` (not `applied`). Match the existing rollback-test harness in
  `worker_test.go` (reuse its fakes: services/updates/snapshots/runner). Assert
  via the fake/real updates store that the update is `rolled_back` after
  `runRollback` succeeds.

- [ ] **Step 2: Run red** → FAIL (currently stays `applied`).

- [ ] **Step 3: Implement**: in `rollbackPullUp`'s success tail, right where it
  emits the `rolled_back` event (near the old "we intentionally LEAVE ... applied"
  note), add:
  ```go
  if err := a.updates.MarkRolledBack(svc.ID, svc.CurrentDigest); err != nil {
  	a.emit(job, "system", "warning: mark rolled_back failed: "+err.Error())
  }
  ```
  `svc.CurrentDigest` here is the pre-rollback applied target (svc was loaded
  before `UpdateRuntime`). Replace the stale NOTE comment: the rollback now marks
  the reverted update `rolled_back` explicitly (RecordDrift preserves it, so
  auto-update never re-applies it: the rollback-respect invariant now holds via
  status, not via `applied` preservation). A mark failure is warn-and-continue
  (must not flip an already-successful rollback to failed).

- [ ] **Step 4: Run green**: `go test ./internal/job/` → PASS (existing rollback
  tests too). `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/job/worker.go internal/job/worker_test.go
git commit -m "feat(job): rollback marks the reverted update rolled_back"
```

---

### Task 3: Discovery: invalidate detect cache on running-digest change

**Files:**
- Modify: `internal/store/images.go` (`RemoteStates.Invalidate`)
- Modify: `internal/discovery/discovery.go` (Reconciler states dep + invalidate on change)
- Modify: `cmd/dockbrr/main.go` (wire states into `NewReconciler`)
- Test: `internal/store/images_test.go`, `internal/discovery/discovery_test.go`

**Interfaces produced:** `RemoteStates.Invalidate(repo, tag string) error`; `NewReconciler(..., states *store.RemoteStates)`.

- [ ] **Step 1: Failing tests**
  - store: `Invalidate(repo, tag)` deletes the row; `Get` then returns `ErrRemoteStateNotFound`.
  - discovery: a present service whose newly-collected digest DIFFERS from its
    stored `current_digest` triggers `states.Invalidate(repo, tag)`; an UNCHANGED
    digest does not; a brand-new service (no stored digest) does not. Use a spy
    RemoteStates (or a real store + assert the row is gone), match the discovery
    harness. No sleeps.

- [ ] **Step 2: Run red** → FAIL.

- [ ] **Step 3: Implement**
  - `RemoteStates.Invalidate`:
    ```go
    // Invalidate drops the cached remote state for (repo, tag) so the next detect
    // does a full network resolve + semver scan instead of the digest-only
    // short-circuit. Called when a service's running image changed (recreate).
    func (r *RemoteStates) Invalidate(repo, tag string) error {
    	_, err := r.db.Exec(`DELETE FROM image_remote_state WHERE repo=? AND tag=?`, repo, tag)
    	return err
    }
    ```
  - `Reconciler`: add `states *store.RemoteStates` field + param to `NewReconciler`
    (nil disables invalidation). Update `cmd/dockbrr/main.go` to pass the
    remote-states store.
  - In `Reconcile`, before/at the service upsert, obtain the service's PRIOR
    stored digest. Build a per-project `map[name]storedDigest` from
    `services.ListByProject(pid)` (the reconcile already lists stored services for
    the prune pass: reuse or fetch once), then for each discovered service `s`:
    ```go
    if r.states != nil {
    	prev := storedDigest[s.Name] // "" if new/unknown
    	if prev != "" && prev != s.CurrentDigest {
    		repo, tag := splitRef(s.ImageRef) // reuse the discovery-local ref split (declaredDiffers area) or detect.SplitRef
    		_ = r.states.Invalidate(repo, tag) // best-effort; log on error
    	}
    }
    ```
    Do this for both discovered and manual projects (a manual project's service
    can also be recreated). Keep it best-effort (a failed invalidate must not fail
    the reconcile). Set `changed=true` is NOT required for invalidation (it doesn't
    alter the discovered surface).

- [ ] **Step 4: Run green**: `go test ./internal/store/ ./internal/discovery/` → PASS. `CGO_ENABLED=0 go build ./...`; `go vet ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/store/images.go internal/store/images_test.go internal/discovery/discovery.go internal/discovery/discovery_test.go cmd/dockbrr/main.go
git commit -m "feat(discovery): invalidate detect cache on running-digest change"
```

---

### Task 4: Frontend: rolled-back badge + restore

**Files:**
- Modify: `web/src/components/StatusBadge.tsx` (union, LABEL, VARIANT, computeStatus)
- Modify: `web/src/components/DashboardTable.tsx` (pass rolled-back through computeStatus)
- Modify: `web/src/components/ReviewDrawer.tsx` (show Restore for rolled_back)
- Test: `web/src/components/StatusBadge.test.tsx`, `web/src/components/ReviewDrawer.test.tsx`

**Interfaces:** consumes the update `status` (now possibly `"rolled_back"`) from the dashboard list (Task 1).

- [ ] **Step 1: Failing tests**
  - `computeStatus` returns `"rolled-back"` when the update status is
    `rolled_back` (mirror the dismissed test).
  - `StatusBadge` renders "Rolled back" (grey) for the `"rolled-back"` status.
  - `ReviewDrawer` shows the **Restore** button when `update.status === "rolled_back"`
    and clicking it calls restore (reuse the dismissed-restore test as template).

- [ ] **Step 2: Run red**: `cd web && npm test -- 'StatusBadge|ReviewDrawer'` → FAIL.

- [ ] **Step 3: Implement**
  - `StatusBadge.tsx`: add `| "rolled-back"` to `Status`; `LABEL["rolled-back"] = "Rolled back"`; `VARIANT["rolled-back"] = "default"` (grey, same as dismissed). In `computeStatus`, add after the update-available/dismissed branch:
    `if (update?.status === "rolled_back") return "rolled-back";`
    (Match how the `dismissed` branch reads the update status, extend the
    `update` param shape used by `computeStatus`/`DashboardTable` to carry the raw
    status or a `rolledBack` flag, mirroring the existing `dismissed` wiring.)
  - `DashboardTable.tsx`: where it builds the `update` arg for `computeStatus`
    (currently maps `status === "available"` → open, `=== "dismissed"` → dismissed),
    also surface `rolled_back` (add `rolledBack: r.update.status === "rolled_back"`
    or pass the raw status: match the chosen computeStatus shape).
  - `ReviewDrawer.tsx`: the Restore button currently renders when
    `update.status === "dismissed"`; change the condition to also include
    `"rolled_back"` (`update.status === "dismissed" || update.status === "rolled_back"`).
    `useRestore`/`handleRestore` unchanged.

- [ ] **Step 4: Run green**: `cd web && npm test -- 'StatusBadge|ReviewDrawer'` → PASS; then `./node_modules/.bin/tsc -b --noEmit && npm run build`; `git checkout -- internal/httpapi/dist/index.html`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/StatusBadge.tsx web/src/components/DashboardTable.tsx web/src/components/ReviewDrawer.tsx web/src/components/StatusBadge.test.tsx web/src/components/ReviewDrawer.test.tsx
git commit -m "feat(web): rolled-back badge + restore"
```

---

## Final verification (after all tasks)

- `CGO_ENABLED=0 go build ./...`; `go vet ./... && go test ./...`, green.
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm test && npm run build`, green.
- `git checkout -- internal/httpapi/dist/index.html`.
- Manual smoke (live Docker): apply an update, then `docker compose down && up` on
  the OLD image → within one scan the update re-appears as available (not "up to
  date"). Roll back a successful update → it shows grey **Rolled back**, is NOT
  auto-re-applied, and **Restore** flips it back to available.
