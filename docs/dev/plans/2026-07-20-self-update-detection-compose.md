# Self-update detection under Compose Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect dockbrr's own container id reliably under Docker Compose so the self-guard and watchtower-style self-updater actually arm, route an Apply on dockbrr's own update to the self-updater with a clear confirmation, and let "Check for updates" un-hide a dismissed update notice.

**Architecture:** Backend — replace the hostname-only `SelfContainerID()` with a layered `/proc/self/mountinfo` → `/proc/self/cgroup` → hostname probe (pure parse functions), then have the API route an Apply whose service is dockbrr's own container to a `self_update` job via a shared precondition helper. Frontend — expose `is_self` on the update payload, swap in a self-update-specific `window.confirm` at every apply trigger, and make the sidebar notice's dismissal reactive so a manual check re-shows it.

**Tech Stack:** Go 1.26 (CGO-free), chi, modernc SQLite; React + TS + Vite + TanStack Query + Tailwind; vitest.

## Global Constraints

- `CGO_ENABLED=0` must stay buildable: `/proc` reads use `os.ReadFile` (stdlib), no cgo. (CLAUDE.md invariant 1)
- All Docker mutation flows through the Job Engine; the API only enqueues jobs. (invariant 2)
- Compose verbs whitelisted + exec'd as argv; no new shell strings. (invariant 6)
- No em-dashes in code comments, docs, or UI copy; write plainer.
- No Claude attribution in commits.
- Container id comparisons are prefix-matched in both directions (stored ids may be 12- or 64-hex; the probe may return 64-hex).
- TS typecheck via `cd web && npm run typecheck` (never `npx tsc`).
- Working directory for all commands: the worktree root `.claude/worktrees/self-update-detection-compose`.

---

## Task 1: Robust self-container detection

**Files:**
- Modify: `internal/job/dispatch.go` (replace `SelfContainerID` at :152-168, add parse helpers + imports)
- Test: `internal/job/selfid_test.go` (create)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func SelfContainerID() string` (unchanged signature; now layered)
  - `func parseContainerIDFromMountinfo(content string) string`
  - `func parseContainerIDFromCgroup(content string) string`
  - `func parseContainerIDFromHostname(hostname string) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/job/selfid_test.go`:

```go
package job

import "testing"

func TestParseContainerIDFromMountinfo(t *testing.T) {
	const compose = `650 630 0:64 / /proc rw,nosuid shared:266 - proc proc rw
660 630 259:1 /var/lib/docker/containers/3f2a1b9c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8/hostname /etc/hostname rw,relatime shared:1 - ext4 /dev/root rw
661 630 259:1 /var/lib/docker/overlay2/abcdef0123456789/merged /etc/other rw - ext4 /dev/root rw`
	got := parseContainerIDFromMountinfo(compose)
	want := "3f2a1b9c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
	if got != want {
		t.Fatalf("mountinfo id = %q, want %q", got, want)
	}
	if got := parseContainerIDFromMountinfo("no containers path here\n/overlay2/deadbeef"); got != "" {
		t.Fatalf("expected empty for non-container mountinfo, got %q", got)
	}
}

func TestParseContainerIDFromCgroup(t *testing.T) {
	const v1 = `12:pids:/docker/aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899
11:memory:/docker/aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899`
	want := "aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899"
	if got := parseContainerIDFromCgroup(v1); got != want {
		t.Fatalf("cgroup id = %q, want %q", got, want)
	}
	if got := parseContainerIDFromCgroup("0::/\n0::/init.scope"); got != "" {
		t.Fatalf("expected empty for host cgroup, got %q", got)
	}
}

func TestParseContainerIDFromHostname(t *testing.T) {
	cases := map[string]string{
		"abcdef123456": "abcdef123456", // 12-hex docker run default
		"dockbrr":      "",             // compose service name
		"":             "",
		"abcdef12345g": "",             // non-hex char
		"abcdef1234567": "",            // 13 chars
	}
	for in, want := range cases {
		if got := parseContainerIDFromHostname(in); got != want {
			t.Errorf("hostname(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/job/ -run 'TestParseContainerID' -v`
Expected: FAIL — `undefined: parseContainerIDFromMountinfo` (and siblings).

- [ ] **Step 3: Implement the parse helpers + layered wrapper**

In `internal/job/dispatch.go`, add `"os"` (already imported), `"regexp"` to the import block. Replace the existing `SelfContainerID` function (currently at :152-168) with:

```go
// containerIDRe matches a full 64-hex Docker container id.
var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// parseContainerIDFromMountinfo extracts dockbrr's container id from
// /proc/self/mountinfo. Docker bind-mounts /etc/hostname, /etc/hosts and
// /etc/resolv.conf from /var/lib/docker/containers/<id>/, so the id is the
// 64-hex segment immediately after "/containers/". Overlay layer hashes also
// appear in mountinfo, so anchoring on "/containers/" avoids matching those.
func parseContainerIDFromMountinfo(content string) string {
	const marker = "/containers/"
	for _, line := range strings.Split(content, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := line[i+len(marker):]
		if id := containerIDRe.FindString(rest); id != "" && strings.HasPrefix(rest, id) {
			return id
		}
	}
	return ""
}

// parseContainerIDFromCgroup extracts the id from a cgroup v1 /proc/self/cgroup,
// whose lines carry ".../docker/<id>" or ".../docker-<id>.scope". Only
// container-related 64-hex ids appear here, so the first match wins. Returns ""
// for a host (cgroup v2 root "0::/") where no container id is present.
func parseContainerIDFromCgroup(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if id := containerIDRe.FindString(line); id != "" {
			return id
		}
	}
	return ""
}

// parseContainerIDFromHostname returns the hostname when it is Docker's default
// short container id (exactly 12 hex chars), else "". Under `docker run` the
// hostname is the short id; under Compose it is the service name, so this is the
// last-resort probe.
func parseContainerIDFromHostname(hostname string) string {
	if len(hostname) != 12 {
		return ""
	}
	for _, c := range hostname {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return hostname
}

// SelfContainerID returns dockbrr's own container id when running inside a
// container, or "" on a host install (guard + self-updater stay disabled).
// Probes in order of reliability: the containers bind-mount path in
// /proc/self/mountinfo, then cgroup v1, then the hostname short-id convention.
// A file that cannot be read is treated as "no match" and falls through.
func SelfContainerID() string {
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		if id := parseContainerIDFromMountinfo(string(b)); id != "" {
			return id
		}
	}
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if id := parseContainerIDFromCgroup(string(b)); id != "" {
			return id
		}
	}
	if h, err := os.Hostname(); err == nil {
		if id := parseContainerIDFromHostname(h); id != "" {
			return id
		}
	}
	return ""
}
```

Remove the now-obsolete doc comment block that preceded the old `SelfContainerID` if it duplicates the new one.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/job/ -run 'TestParseContainerID' -v`
Expected: PASS (all three).

- [ ] **Step 5: Full job package + vet**

Run: `CGO_ENABLED=0 go build ./... && go vet ./internal/job/ && go test ./internal/job/`
Expected: build succeeds, vet clean, existing `dispatch_test.go` guard tests still PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/job/dispatch.go internal/job/selfid_test.go
git commit -m "fix(job): detect own container via mountinfo/cgroup, not just hostname"
```

---

## Task 2: Route Apply-on-self to the self-updater (shared precondition helper)

**Files:**
- Modify: `internal/httpapi/selfupdate.go` (add `serviceIsSelf`, `enqueueSelfUpdate`; refactor `handleSelfUpdateApply`)
- Modify: `internal/httpapi/updates.go:73` (`handleApply` self-routing)
- Test: `internal/httpapi/selfupdate_test.go` (extend), `internal/httpapi/actions_test.go` (extend, if apply routing fits there) or a new `internal/httpapi/apply_self_test.go`

**Interfaces:**
- Consumes: `Deps.SelfID string`, `Deps.SelfUpdate`, `Deps.Jobs`, `Deps.Engine`, `store.Service.ContainerIDs`.
- Produces:
  - `func (s *Server) serviceIsSelf(svc store.Service) bool`
  - `func (s *Server) enqueueSelfUpdate(ctx context.Context) (jobID int64, status int, err error)` — `status == 0` means enqueued (or an existing single-flight job returned); non-zero is the HTTP code to write with `err`.

- [ ] **Step 1: Write the failing test**

Add to `internal/httpapi/selfupdate_test.go` (mirror the existing fixtures there for `Deps` wiring; a self-update is available, `SelfID` set, and a service whose container id prefix-matches `SelfID`):

```go
func TestHandleApplyRoutesSelfToSelfUpdate(t *testing.T) {
	// Arrange a Server whose Deps.SelfID matches a service's container id, an
	// update exists for that service, and the checker reports an update.
	srv, deps := newSelfUpdateTestServer(t) // existing/extended helper: wires fake Engine, Jobs, SelfUpdate, Services, Updates, Projects
	deps.SelfID = "3f2a1b9c4d5e"
	svcID := seedServiceWithContainer(t, deps, "3f2a1b9c4d5e6f70...") // container id prefixed by SelfID
	updID := seedUpdateForService(t, deps, svcID)

	rr := doPost(t, srv, "/api/updates/"+itoa(updID)+"/apply", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		JobID      int64 `json:"job_id"`
		SelfUpdate bool  `json:"self_update"`
	}
	mustJSON(t, rr, &body)
	if !body.SelfUpdate {
		t.Fatalf("expected self_update:true in response")
	}
	if got := deps.Engine.LastEnqueued().Type; got != "self_update" {
		t.Fatalf("enqueued job type = %q, want self_update", got)
	}
}

func TestHandleApplyNonSelfEnqueuesApply(t *testing.T) {
	srv, deps := newSelfUpdateTestServer(t)
	deps.SelfID = "3f2a1b9c4d5e"
	svcID := seedServiceWithContainer(t, deps, "ffffffffffff0000...") // does NOT match SelfID
	updID := seedUpdateForService(t, deps, svcID)

	rr := doPost(t, srv, "/api/updates/"+itoa(updID)+"/apply", "")

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}
	if got := deps.Engine.LastEnqueued().Type; got != "apply" {
		t.Fatalf("enqueued job type = %q, want apply", got)
	}
}
```

Note: reuse the existing test scaffolding in `selfupdate_test.go`/`actions_test.go`. If helper names differ, adapt these calls to the real fakes (fake `Engine` recording the last enqueued `store.Job`, in-memory `store.Services`/`store.Updates`/`store.Projects` from a temp SQLite db as other httpapi tests do). Keep the two assertions: self → `self_update` + `self_update:true`; non-self → `apply` + 202.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run 'TestHandleApply.*Self' -v`
Expected: FAIL — `handleApply` still enqueues `apply` for the self case / `self_update` field absent.

- [ ] **Step 3: Implement `serviceIsSelf` + `enqueueSelfUpdate` and refactor the self-update handler**

In `internal/httpapi/selfupdate.go`, add imports `"context"` and `"strings"`. Add:

```go
// serviceIsSelf reports whether svc is dockbrr's own container. False on a host
// install (SelfID == ""). Ids may be stored 12- or 64-hex while SelfID may be
// 64-hex, so prefix-match both directions (same rule as job.Dispatcher).
func (s *Server) serviceIsSelf(svc store.Service) bool {
	self := s.deps.SelfID
	if self == "" {
		return false
	}
	for _, cid := range svc.ContainerIDs {
		if strings.HasPrefix(cid, self) || strings.HasPrefix(self, cid) {
			return true
		}
	}
	return false
}

// enqueueSelfUpdate runs the self_update preconditions and enqueues the job,
// shared by the manual endpoint and the Apply-on-self route. status == 0 means
// success (jobID is a new or an already-active single-flight job); a non-zero
// status is the HTTP code to write alongside err. A StatusInternalServerError
// status signals the caller to use writeInternalError.
func (s *Server) enqueueSelfUpdate(ctx context.Context) (int64, int, error) {
	if s.deps.SelfID == "" {
		return 0, http.StatusConflict, errors.New("self-update is only available when dockbrr runs in a container")
	}
	if s.deps.SelfUpdate == nil {
		return 0, http.StatusConflict, errors.New("self-update is unavailable")
	}
	res, err := s.deps.SelfUpdate.Check(ctx)
	if err != nil {
		return 0, http.StatusConflict, errors.New("could not check for updates, try again later")
	}
	if !res.UpdateAvailable {
		return 0, http.StatusConflict, errors.New("no dockbrr update is available")
	}
	if s.deps.Jobs != nil {
		if active, ok, err := s.deps.Jobs.ActiveByType("self_update"); err != nil {
			return 0, http.StatusInternalServerError, err
		} else if ok {
			return active.ID, 0, nil // single-flight: return the existing job
		}
	}
	id, err := s.deps.Engine.Enqueue(store.Job{Type: "self_update", RequestedBy: "user"})
	if err != nil {
		return 0, http.StatusInternalServerError, err
	}
	return id, 0, nil
}
```

Replace the body of `handleSelfUpdateApply` with the thin wrapper:

```go
func (s *Server) handleSelfUpdateApply(w http.ResponseWriter, r *http.Request) {
	id, status, err := s.enqueueSelfUpdate(r.Context())
	if err != nil {
		if status == http.StatusInternalServerError {
			writeInternalError(w, "enqueue self_update", err)
		} else {
			writeJSONError(w, status, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": id})
}
```

In `internal/httpapi/updates.go` `handleApply`, insert the self-route immediately after the `svc.State == "gone"` guard (currently :102-105), before `scope := scopeFromBody(r)`:

```go
	if s.serviceIsSelf(svc) {
		id, status, err := s.enqueueSelfUpdate(r.Context())
		if err != nil {
			if status == http.StatusInternalServerError {
				writeInternalError(w, "enqueue self_update", err)
			} else {
				writeJSONError(w, status, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"job_id": id, "self_update": true})
		return
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ -run 'TestHandleApply.*Self|TestHandleSelfUpdateApply' -v`
Expected: PASS (routing tests + the retargeted single-flight/precondition tests).

- [ ] **Step 5: Full httpapi package**

Run: `go vet ./internal/httpapi/ && go test ./internal/httpapi/`
Expected: vet clean, all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/selfupdate.go internal/httpapi/updates.go internal/httpapi/*_test.go
git commit -m "feat(api): route apply on dockbrr's own container to self_update"
```

---

## Task 3: Expose `is_self` on the update payload

**Files:**
- Modify: `internal/httpapi/updates.go` (`updateDTO` :13-27, `handleListUpdates` loop :36-, `handleListLastApplied` loop :59-)
- Test: `internal/httpapi/updates_test.go` (create or extend, if present)

**Interfaces:**
- Consumes: `serviceIsSelf` (Task 2), `Deps.Services`.
- Produces: `updateDTO.IsSelf bool` (`json:"is_self"`).

- [ ] **Step 1: Write the failing test**

Add to the httpapi update-list tests (extend the existing suite; if none, create `internal/httpapi/updates_test.go`):

```go
func TestListUpdatesMarksSelf(t *testing.T) {
	srv, deps := newSelfUpdateTestServer(t)
	deps.SelfID = "3f2a1b9c4d5e"
	selfSvc := seedServiceWithContainer(t, deps, "3f2a1b9c4d5e6f70...")
	otherSvc := seedServiceWithContainer(t, deps, "ffffffffffff0000...")
	seedUpdateForService(t, deps, selfSvc)
	seedUpdateForService(t, deps, otherSvc)

	rr := doGet(t, srv, "/api/updates")
	var out []struct {
		ServiceID int64 `json:"service_id"`
		IsSelf    bool  `json:"is_self"`
	}
	mustJSON(t, rr, &out)

	byID := map[int64]bool{}
	for _, u := range out {
		byID[u.ServiceID] = u.IsSelf
	}
	if !byID[selfSvc] {
		t.Errorf("self service update is_self = false, want true")
	}
	if byID[otherSvc] {
		t.Errorf("non-self service update is_self = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run 'TestListUpdatesMarksSelf' -v`
Expected: FAIL — `is_self` field missing / always false.

- [ ] **Step 3: Add the field and populate it**

In `updateDTO` add after `DetectedAt`:

```go
	IsSelf bool `json:"is_self"`
```

In both `handleListUpdates` and `handleListLastApplied`, inside the `for _, u := range ups` loop, before appending, resolve the service and compute self:

```go
		isSelf := false
		if svc, err := s.deps.Services.Get(u.ServiceID); err == nil {
			isSelf = s.serviceIsSelf(svc)
		}
```

and set `IsSelf: isSelf` in the `updateDTO{...}` literal. (A service load error leaves `is_self` false; it never fails the list.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/httpapi/ -run 'TestListUpdatesMarksSelf' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/updates.go internal/httpapi/updates_test.go
git commit -m "feat(api): mark dockbrr's own update with is_self in the updates list"
```

---

## Task 4: Self-update job announces the special path

**Files:**
- Modify: `internal/job/selfupdate.go` (`Handle`, near :74-80)
- Test: `internal/job/selfupdate_test.go` (extend, if it asserts emitted messages) — otherwise a light assertion via the existing emitter fake.

**Interfaces:**
- Consumes: existing `emit` closure in `Handle`.
- Produces: no new symbol; one added log line.

- [ ] **Step 1: Add the announcement emit**

In `SelfUpdater.Handle`, immediately after the `res.UpdateAvailable` check passes and before resolving `currentRef` (i.e. right after the `if !res.UpdateAvailable { ... }` block at :66-73), add:

```go
	emit("this is dockbrr updating itself: the new image is pulled, then a short-lived helper swaps the container. A normal compose apply is not used here, and dockbrr will restart.")
```

- [ ] **Step 2: Verify build + existing self-update tests**

Run: `go test ./internal/job/ -run 'SelfUpdate' -v`
Expected: PASS (existing tests unaffected; message-order assertions, if any, updated to include the new line).

- [ ] **Step 3: Commit**

```bash
git add internal/job/selfupdate.go internal/job/selfupdate_test.go
git commit -m "feat(job): self_update job logs that it is a self-update swap"
```

---

## Task 5: Frontend types + shared self-update copy

**Files:**
- Modify: `web/src/api/types.ts` (`Update` interface :28-)
- Create: `web/src/lib/selfUpdate.ts`

**Interfaces:**
- Produces:
  - `Update.is_self: boolean`
  - `web/src/lib/selfUpdate.ts` exporting `export const SELF_UPDATE_CONFIRM: string`

- [ ] **Step 1: Add `is_self` to the Update type**

In `web/src/api/types.ts`, inside `export interface Update`, add:

```ts
  is_self: boolean;
```

Also widen the apply mutation response where typed (Task 6 uses it): the `useApply` return type becomes `{ job_id: number; self_update?: boolean }`.

- [ ] **Step 2: Create the shared confirm copy**

Create `web/src/lib/selfUpdate.ts`:

```ts
// Confirmation shown before applying an update to dockbrr itself. The self-update
// swaps the running container via a detached helper, so the browser connection
// drops and reconnects on the new version. Reused by every apply trigger.
export const SELF_UPDATE_CONFIRM =
  "Update dockbrr itself? dockbrr will pull the new image and hand the container swap to a short-lived helper, then restart. This page will briefly disconnect and reconnect on the new version. Continue?";
```

- [ ] **Step 3: Typecheck**

Run: `cd web && npm run typecheck`
Expected: type errors in components that construct `Update` fixtures without `is_self` — those are fixed in Task 6 tests; for now expect the new files themselves to typecheck. If test fixtures fail, note them and proceed (Task 6 updates fixtures).

- [ ] **Step 4: Commit**

```bash
git add web/src/api/types.ts web/src/lib/selfUpdate.ts
git commit -m "feat(web): add is_self to Update type and shared self-update confirm copy"
```

---

## Task 6: Self-update confirmation at every apply trigger

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (:199-212 confirm+mutate)
- Modify: `web/src/components/ReviewDrawer.tsx` (`handleApply` :46-)
- Modify: `web/src/components/BulkActions.tsx` (:105-110 confirm)
- Modify: `web/src/components/layout/UpdateNotice.tsx` ("Update now" onClick :79)
- Test: the co-located `*.test.tsx` for each (extend)

**Interfaces:**
- Consumes: `SELF_UPDATE_CONFIRM` (Task 5), `Update.is_self`.
- Produces: no new exports.

- [ ] **Step 1: Write the failing tests**

In `web/src/components/DashboardTable.test.tsx` (and mirror the idea in ReviewDrawer/BulkActions tests), add a case that a self update uses the self confirm text. Example shape (adapt to the file's existing render helpers and fixtures; set `is_self: true` on the update fixture):

```tsx
it("uses the self-update confirm message when applying dockbrr's own update", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  renderDashboardWithUpdate({ is_self: true }); // existing helper + is_self override
  await userEvent.click(screen.getByLabelText(/Apply update to/i));
  expect(confirm).toHaveBeenCalledWith(expect.stringContaining("Update dockbrr itself?"));
  confirm.mockRestore();
});
```

For `UpdateNotice.test.tsx`, add: clicking "Update now" calls `window.confirm` and only mutates on confirm.

- [ ] **Step 2: Run to verify they fail**

Run: `cd web && npm test -- DashboardTable UpdateNotice`
Expected: FAIL — current code shows the recreate message / no confirm on Update now.

- [ ] **Step 3: Implement the self confirm at each site**

`DashboardTable.tsx` (:203 area) — replace the `msg` computation:

```tsx
                const msg = update.is_self
                  ? SELF_UPDATE_CONFIRM
                  : service.pinned
                    ? `Apply update to "${service.name}"? It is pinned, and applying overrides the pin and recreates the container.`
                    : `Apply update to "${service.name}"? This recreates the container.`;
                if (!window.confirm(msg)) return;
```

(`update` is already in scope; add `import { SELF_UPDATE_CONFIRM } from "@/lib/selfUpdate";`.)

`ReviewDrawer.tsx` `handleApply` — guard before `apply.mutate`:

```tsx
  function handleApply() {
    if (update.is_self && !window.confirm(SELF_UPDATE_CONFIRM)) return;
    apply.mutate(
      { id: update.id, scope: SCOPE },
      // ...unchanged
    );
  }
```

`BulkActions.tsx` (:107) — when any pending update targets self, extend the confirm text:

```tsx
        const anySelf = pending.some((u) => u.is_self);
        const base = `Apply ${n} available update${n > 1 ? "s" : ""} ${scopeNoun}? Each affected service is recreated individually.`;
        const msg = anySelf ? `${base} This includes dockbrr itself, which will restart and briefly disconnect this page.` : base;
        if (!window.confirm(msg)) return;
```

(Confirm `pending` items carry `is_self`; if `pending` is a narrower type than `Update`, thread `is_self` through the same list that already carries `service_id`.)

`UpdateNotice.tsx` — the "Update now" button always self:

```tsx
            onClick={() => {
              if (window.confirm(SELF_UPDATE_CONFIRM)) apply.mutate();
            }}
```

Add the `SELF_UPDATE_CONFIRM` import to each file. Update any test fixtures that build an `Update` to include `is_self: false` by default.

- [ ] **Step 4: Run tests + typecheck**

Run: `cd web && npm test -- DashboardTable ReviewDrawer BulkActions UpdateNotice && npm run typecheck`
Expected: PASS, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/components web/src/lib/selfUpdate.ts
git commit -m "feat(web): confirm self-update before applying dockbrr's own update"
```

---

## Task 7: Reactive dismissal + "Check for updates" re-shows the notice

**Files:**
- Create: `web/src/hooks/useDismissedUpdate.ts`
- Modify: `web/src/components/layout/UpdateNotice.tsx` (dismiss state)
- Modify: `web/src/hooks/mutations.ts` (`useCheckForUpdates` :206-213)
- Test: `web/src/hooks/useDismissedUpdate.test.ts` (create), `web/src/components/layout/UpdateNotice.test.tsx` (extend), `web/src/hooks/mutations.test.tsx` (extend)

**Interfaces:**
- Produces:
  - `useDismissedUpdate(): { dismissed: string | null; dismiss: (latest: string) => void }`
  - `clearDismissedUpdate(): void`
  - `DISMISS_KEY` (moved here; re-exported from `UpdateNotice` for backward-compat with its test)

- [ ] **Step 1: Write the failing tests**

Create `web/src/hooks/useDismissedUpdate.test.ts`:

```ts
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useDismissedUpdate, clearDismissedUpdate, DISMISS_KEY } from "./useDismissedUpdate";

afterEach(() => localStorage.clear());

describe("useDismissedUpdate", () => {
  it("reflects a dismiss and re-renders on an external clear", () => {
    const { result } = renderHook(() => useDismissedUpdate());
    expect(result.current.dismissed).toBeNull();

    act(() => result.current.dismiss("0.8.0"));
    expect(result.current.dismissed).toBe("0.8.0");
    expect(localStorage.getItem(DISMISS_KEY)).toBe("0.8.0");

    act(() => clearDismissedUpdate());
    expect(result.current.dismissed).toBeNull();
  });
});
```

Extend `mutations.test.tsx` with a case that `useCheckForUpdates` success removes `DISMISS_KEY`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && npm test -- useDismissedUpdate`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement the hook**

Create `web/src/hooks/useDismissedUpdate.ts`:

```ts
import { useSyncExternalStore } from "react";

// Per-version dismissal of the sidebar update notice, persisted in localStorage.
// Backed by useSyncExternalStore so every mount re-renders when the value changes
// (a dismiss, an external clear from "Check for updates", or another tab).
export const DISMISS_KEY = "dockbrr_dismissed_update";
const CHANGED_EVENT = "dockbrr:dismiss-changed";

function subscribe(cb: () => void): () => void {
  window.addEventListener(CHANGED_EVENT, cb);
  window.addEventListener("storage", cb);
  return () => {
    window.removeEventListener(CHANGED_EVENT, cb);
    window.removeEventListener("storage", cb);
  };
}

function getSnapshot(): string | null {
  return localStorage.getItem(DISMISS_KEY);
}

export function useDismissedUpdate() {
  const dismissed = useSyncExternalStore(subscribe, getSnapshot);
  const dismiss = (latest: string) => {
    localStorage.setItem(DISMISS_KEY, latest);
    window.dispatchEvent(new Event(CHANGED_EVENT));
  };
  return { dismissed, dismiss };
}

// clearDismissedUpdate un-hides the notice (used by the manual "Check for
// updates" action so a previously dismissed update reappears).
export function clearDismissedUpdate(): void {
  localStorage.removeItem(DISMISS_KEY);
  window.dispatchEvent(new Event(CHANGED_EVENT));
}
```

- [ ] **Step 4: Wire UpdateNotice to the hook**

In `web/src/components/layout/UpdateNotice.tsx`, replace the `useState`/`localStorage.getItem` dismissal (:20) and the `DISMISS_KEY` const (:9) with the hook, and re-export the key for the existing test import:

```tsx
import { useDismissedUpdate } from "@/hooks/useDismissedUpdate";
export { DISMISS_KEY } from "@/hooks/useDismissedUpdate";
// ...
export function UpdateNotice({ collapsed }: { collapsed: boolean }) {
  const { data } = useSelfUpdate();
  const apply = useApplySelfUpdate();
  const { dismissed, dismiss: setDismissed } = useDismissedUpdate();

  if (!data?.update_available) return null;
  if (dismissed === data.latest) return null;

  const dismiss = () => setDismissed(data.latest!);
  // ...rest unchanged (dismiss() still wired to the X button)
```

Remove the now-unused `useState` import if nothing else uses it.

- [ ] **Step 5: Clear the dismissal on manual check**

In `web/src/hooks/mutations.ts` `useCheckForUpdates`, import `clearDismissedUpdate` and call it on success:

```ts
import { clearDismissedUpdate } from "@/hooks/useDismissedUpdate";
// ...
    onSuccess: (data) => {
      qc.setQueryData(keys.selfUpdate, data);
      clearDismissedUpdate();
    },
```

- [ ] **Step 6: Run tests + typecheck**

Run: `cd web && npm test -- useDismissedUpdate UpdateNotice mutations && npm run typecheck`
Expected: PASS, no type errors. `UpdateNotice.test.tsx` DISMISS_KEY import still resolves via the re-export.

- [ ] **Step 7: Commit**

```bash
git add web/src/hooks/useDismissedUpdate.ts web/src/hooks/useDismissedUpdate.test.ts web/src/components/layout/UpdateNotice.tsx web/src/hooks/mutations.ts web/src/hooks/mutations.test.tsx
git commit -m "feat(web): re-show dismissed update notice on manual check for updates"
```

---

## Task 8: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Backend build + vet + tests**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: build succeeds (static, CGO-free), vet clean, all tests PASS.

- [ ] **Step 2: Web tests + typecheck + build**

Run: `cd web && npm test && npm run typecheck && npm run build`
Expected: all vitest PASS, no type errors, build succeeds.

- [ ] **Step 3: One-shot check task**

Run: `mise run check`
Expected: go vet + go test + web vitest all green.

- [ ] **Step 4: Manual smoke (documented, not automated)**

Confirm the intended behavior for the reviewer to verify in a real compose deploy:
- With dockbrr run via compose (hostname = service name), `SelfContainerID()` now returns a 64-hex id (guard + self-updater armed).
- Apply on dockbrr's own row → self confirm → a `self_update` job runs (helper swap), not a compose recreate.
- Apply on any other service → unchanged.
- Dismiss the sidebar notice, click "Check for updates" → notice reappears.
