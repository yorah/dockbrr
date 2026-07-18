# Self-update apply + local-image awareness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add in-app watchtower-style self-update (dockbrr replaces its own container via a detached helper) and local-image awareness (compose `build:` images classified `local`, skipped by registry probes, badged on the dashboard).

**Architecture:** Part A splits the risky pull (in-process, recoverable) from the unrecoverable swap (a detached helper container running dockbrr's own image with a `self-update-swap` subcommand, which stop/remove/create-from-inspect/start the old container with the new image, rolling back to the old image on failure). Part B derives `image_local` from the compose `build:` directive at discovery time, persists it per service, short-circuits detection for local images, and surfaces a distinct `local` status on the dashboard.

**Tech Stack:** Go 1.26 (CGO_ENABLED=0), Docker SDK, SQLite, React + TS + Tailwind + vitest.

## Global Constraints

- CGO_ENABLED=0, single static binary; SPA embedded via embed.FS. No new runtime deps.
- Invariant 2: only the Job Engine mutates Docker. The `self-update-swap` helper is a separately-spawned process (not the API/UI), consistent with this.
- Conventional Commits. No Claude/AI attribution in commits.
- TS typecheck via `npm run typecheck` (never `npx tsc`). Full build backstop: `cd web && npm run build`.
- Go checks: `go vet ./... && go test ./...` (or `mise run check`). Frontend: `cd web && npm test`.
- No em-dashes in any code comments, docs, or UI copy.
- Frontend: changelog/markdown via rehype-sanitize, no `dangerouslySetInnerHTML`; no CDN.

---

# PART B: Local-image awareness (Tasks 1-6)

### Task 1: Surface `build` from the compose parser

**Files:**
- Modify: `internal/compose/parse.go` (Service struct + Parse mapping)
- Test: `internal/compose/parse_test.go`

**Interfaces:**
- Produces: `compose.Service.Build bool` (true when the compose service declares a `build:` section).

- [ ] **Step 1: Write the failing test**

Add to `internal/compose/parse_test.go`:

```go
func TestParseMarksBuildServiceLocal(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(file, []byte(`
services:
  api:
    build: .
  cache:
    image: redis:7.2.0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Parse(context.Background(), dir, []string{file})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range proj.Services {
		got[s.Name] = s.Build
	}
	if !got["api"] {
		t.Errorf("api: Build = false, want true (has build:)")
	}
	if got["cache"] {
		t.Errorf("cache: Build = true, want false (image only)")
	}
}
```

(Confirm `filepath`, `os`, `context` are imported in the test file; add any missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/compose/ -run TestParseMarksBuildServiceLocal`
Expected: FAIL (`s.Build` undefined).

- [ ] **Step 3: Implement**

In `internal/compose/parse.go`, add `Build bool` to the `Service` struct (after `Pid string`), with a comment:

```go
	// Build is true when the service declares a compose `build:` section, i.e.
	// its image is built locally and has no registry to check for updates.
	Build       bool
```

In `Parse`, set it in the append (compose-go exposes `svc.Build` as a `*types.BuildConfig`):

```go
		out.Services = append(out.Services, Service{
			Name: name, Image: svc.Image,
			NetworkMode: svc.NetworkMode, Ipc: svc.Ipc, Pid: svc.Pid,
			Build: svc.Build != nil,
		})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/compose/ -run TestParseMarksBuildServiceLocal`
Expected: PASS. Then `go test ./internal/compose/` (full package) PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/compose/parse.go internal/compose/parse_test.go
git commit -m "feat(compose): expose build: directive as Service.Build"
```

---

### Task 2: Persist `image_local` on the service row

**Files:**
- Create: `internal/store/migrations/0011_service_image_local.sql`
- Modify: `internal/store/services.go` (Service struct, Upsert, Get, List, ListByProject)
- Test: `internal/store/services_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.Service.ImageLocal bool`; `Services.Upsert` persists it; `Get`/`List`/`ListByProject` scan it.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/services_test.go` (match the existing test setup helpers in that file for opening a store and creating a project; mirror an existing test's boilerplate):

```go
func TestServiceImageLocalRoundTrips(t *testing.T) {
	db := newTestDB(t) // use whatever the file's existing helper is named
	projects := NewProjects(db)
	services := NewServices(db)
	pid, err := projects.Upsert(Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := services.Upsert(Service{ProjectID: pid, Name: "api", ImageRef: "api:dev", CurrentDigest: "sha256:x", State: "running", ImageLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := services.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ImageLocal {
		t.Fatalf("ImageLocal = false after round-trip, want true")
	}
}
```

(If the file's DB helper differs, e.g. `openTestStore(t)`, use that instead. Read the top of `services_test.go` first.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestServiceImageLocalRoundTrips`
Expected: FAIL (`ImageLocal` undefined).

- [ ] **Step 3: Implement**

Create `internal/store/migrations/0011_service_image_local.sql`:

```sql
ALTER TABLE services ADD COLUMN image_local BOOLEAN NOT NULL DEFAULT 0;
```

In `internal/store/services.go`:

Add to `Service` struct (after `Healthcheck bool`):

```go
	ImageLocal        bool // image built from a compose build: directive (no registry to check)
```

`Upsert`: add `image_local` to the insert column list, the `VALUES` placeholders, and the `ON CONFLICT ... DO UPDATE SET` block, and append `sv.ImageLocal` to the args (before `auto`). The insert becomes:

```go
	err = s.db.QueryRow(
		`INSERT INTO services
		   (project_id, name, container_ids, image_ref, current_digest,
		    current_image_id, image_version, pinned, drifted, state, gone_since, healthcheck, image_local, auto_update_enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)
		 ON CONFLICT(project_id, name) DO UPDATE SET
		   container_ids    = excluded.container_ids,
		   image_ref        = excluded.image_ref,
		   current_digest   = excluded.current_digest,
		   current_image_id = excluded.current_image_id,
		   image_version    = excluded.image_version,
		   pinned           = excluded.pinned,
		   drifted          = excluded.drifted,
		   state            = excluded.state,
		   gone_since       = NULL,
		   healthcheck      = excluded.healthcheck,
		   image_local      = excluded.image_local,
		   updated_at       = CURRENT_TIMESTAMP
		 RETURNING id`,
		sv.ProjectID, sv.Name, string(cidsJSON), sv.ImageRef, sv.CurrentDigest,
		sv.CurrentImageID, sv.ImageVersion, sv.Pinned, sv.Drifted, sv.State, sv.Healthcheck, sv.ImageLocal, auto,
	).Scan(&id)
```

`Get`, `List`, `ListByProject`: add `image_local` to each SELECT column list (right after `healthcheck`) and add `&sv.ImageLocal` to each `Scan` at the matching position (right after `&sv.Healthcheck`). There are three SELECTs; update all three. Example for `Get`:

```go
	err := s.db.QueryRow(
		`SELECT id, project_id, name, container_ids, image_ref, current_digest,
		        current_image_id, image_version, pinned, drifted, state, gone_since, healthcheck, image_local, auto_update_enabled, updated_at
		   FROM services WHERE id=?`,
		id,
	).Scan(
		&sv.ID, &sv.ProjectID, &sv.Name, &cidsJSON, &sv.ImageRef, &sv.CurrentDigest,
		&sv.CurrentImageID, &sv.ImageVersion, &sv.Pinned, &sv.Drifted, &sv.State, &goneSince, &sv.Healthcheck, &sv.ImageLocal, &auto, &sv.UpdatedAt,
	)
```

Apply the identical column + scan additions to `List` and `ListByProject`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/`
Expected: PASS (new test + all existing service/store tests, which exercise the same SELECTs).

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/0011_service_image_local.sql internal/store/services.go internal/store/services_test.go
git commit -m "feat(store): persist image_local on services"
```

---

### Task 3: Discovery sets `image_local` from the compose build map

**Files:**
- Modify: `internal/discovery/discovery.go` (Reconcile: build a local map from the compose parse, pass to Upsert)
- Test: `internal/discovery/discovery_test.go`

**Interfaces:**
- Consumes: `compose.Service.Build` (Task 1), `store.Service.ImageLocal` (Task 2).
- Produces: reconciled compose services carry `ImageLocal` = (compose service has `build:`).

- [ ] **Step 1: Write the failing test**

Read `internal/discovery/discovery_test.go` for its existing fake `Collector` and store setup, then add a test that reconciles a compose project whose on-disk compose file has a `build:` service and asserts the stored service's `ImageLocal`. Mirror an existing reconcile test's scaffolding (fake collector returning `docker.Container` with `Project`, `Service`, `WorkingDir`, `ConfigFiles` set to a temp compose file). Core assertion:

```go
func TestReconcileMarksBuildServiceLocal(t *testing.T) {
	// ... set up store + a temp dir with compose.yml containing:
	//   services:
	//     api: { build: . }
	//     cache: { image: redis:7.2.0 }
	// ... fake collector returns two containers (project "app", services "api","cache")
	//     both with WorkingDir=dir and ConfigFiles=[composePath].
	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	svcs, _ := services.ListByProject(pid) // pid resolved from projects.List()
	got := map[string]bool{}
	for _, s := range svcs {
		got[s.Name] = s.ImageLocal
	}
	if !got["api"] {
		t.Errorf("api ImageLocal = false, want true")
	}
	if got["cache"] {
		t.Errorf("cache ImageLocal = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/discovery/ -run TestReconcileMarksBuildServiceLocal`
Expected: FAIL (services come back with `ImageLocal=false`).

- [ ] **Step 3: Implement**

In `internal/discovery/discovery.go` `Reconcile`, the block that parses compose for `declared` (around lines 294-302) also collects the build flags. Change it to:

```go
			declared := map[string]string{} // service name -> declared image ref
			buildLocal := map[string]bool{}  // service name -> has a compose build: directive
			if g.Kind == "compose" && len(g.ConfigFiles) > 0 {
				if pj, perr := compose.Parse(ctx, g.WorkingDir, g.ConfigFiles); perr == nil {
					for _, s := range pj.Services {
						declared[s.Name] = s.Image
						buildLocal[s.Name] = s.Build
					}
				}
				// parse error: leave declared/buildLocal empty -> nothing marked this cycle.
			}
```

Then in the `r.services.Upsert(store.Service{...})` call (around lines 327-340), add the field:

```go
					Drifted:        declaredDiffers(declared[s.Name], s.ImageRef),
					State:          s.State,
					Healthcheck:    s.Healthcheck,
					ImageLocal:     buildLocal[s.Name],
```

(Standalone services never enter the compose branch, so `buildLocal` is empty for them and `ImageLocal` stays false.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/discovery/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/discovery/discovery.go internal/discovery/discovery_test.go
git commit -m "feat(discovery): mark build: services image_local"
```

---

### Task 4: Detect short-circuits local images

**Files:**
- Modify: `internal/detect/detect.go` (Detect: skip network for `ImageLocal`, record `local` state)
- Test: `internal/detect/detect_test.go`

**Interfaces:**
- Consumes: `store.Service.ImageLocal`.
- Produces: for a local service, `Detect` returns `(nil, nil)` with zero resolver calls and upserts `image_remote_state` `Status="local"`.

- [ ] **Step 1: Write the failing test**

Read `internal/detect/detect_test.go` for its fake resolver (it tracks call counts) and store fixtures, then add:

```go
func TestDetectSkipsLocalImage(t *testing.T) {
	// ... build a Detector with a fake resolver that FAILS the test if Resolve is called.
	svc := store.Service{ID: 1, ImageRef: "api:dev", CurrentDigest: "sha256:x", ImageLocal: true}
	upd, err := det.Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if upd != nil {
		t.Fatalf("upd = %+v, want nil (local image)", upd)
	}
	// resolver must not have been called (assert via the fake's counter).
	if resolver.resolveCalls != 0 {
		t.Fatalf("resolver called %d times for a local image, want 0", resolver.resolveCalls)
	}
	// image_remote_state recorded as local.
	st, err := states.Get("api", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "local" {
		t.Fatalf("status = %q, want local", st.Status)
	}
}
```

(Adapt `det`, `resolver`, `states` names to the test file's existing constructor helper. If the fake resolver has no call counter, add one field `resolveCalls int` incremented in its `Resolve`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/detect/ -run TestDetectSkipsLocalImage`
Expected: FAIL (resolver called / status not "local").

- [ ] **Step 3: Implement**

In `internal/detect/detect.go` `Detect`, immediately after the `now := time.Now().UTC()` line (currently line 68, before the cache-hit block), insert:

```go
	// A locally built image (compose build: directive) has no registry to check.
	// Record it as such and skip all network resolution: the periodic scan never
	// probes it, and the dashboard reads it as intentional rather than an error.
	if svc.ImageLocal {
		_ = d.states.Upsert(store.RemoteState{Repo: repo, Tag: tag, Status: "local", ResolvedAt: &now})
		return nil, nil
	}
```

Also update the `RemoteState.Status` field comment in `internal/store/images.go` (line ~118) to include `local`:

```go
	Status         string // ok|rate_limited|error|not_found|local
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/detect/ ./internal/store/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/detect/detect.go internal/detect/detect_test.go internal/store/images.go
git commit -m "feat(detect): classify build: images as local, skip probe"
```

---

### Task 5: API surfaces `image_local` on the service DTO

**Files:**
- Modify: `internal/httpapi/projects.go` (serviceDTO + mapping)
- Test: `internal/httpapi/projects_test.go` (if present; otherwise fold assertion into an existing projects handler test)

**Interfaces:**
- Consumes: `store.Service.ImageLocal`, `check_status="local"`.
- Produces: `serviceDTO.ImageLocal` (`json:"image_local"`).

- [ ] **Step 1: Write the failing test**

Read `internal/httpapi/projects_test.go`. If it exists, add a case asserting a service stored with `ImageLocal: true` serializes `image_local: true` in the `/api/projects` response, and that a `local` `image_remote_state` yields `check_status: "local"`. If no projects handler test file exists, create a minimal one following the pattern of another `httpapi` handler test (they construct a `Server` with an in-memory store and call the handler via `httptest`). Assertion core:

```go
	// after decoding the response into []projectDTO:
	if !resp[0].Services[0].ImageLocal {
		t.Errorf("image_local = false, want true")
	}
	if resp[0].Services[0].CheckStatus != "local" {
		t.Errorf("check_status = %q, want local", resp[0].Services[0].CheckStatus)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run Projects`
Expected: FAIL (`ImageLocal` field undefined on `serviceDTO`).

- [ ] **Step 3: Implement**

In `internal/httpapi/projects.go`, add to `serviceDTO` (after `Healthcheck bool`):

```go
	ImageLocal        bool   `json:"image_local"`
```

In the `sdtos = append(...)` mapping, add:

```go
			ImageLocal:        sv.ImageLocal,
```

(`CheckStatus` already flows through from `image_remote_state`; no change needed there once discovery/detect write `local`.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/httpapi/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/projects.go internal/httpapi/projects_test.go
git commit -m "feat(httpapi): expose image_local on service DTO"
```

---

### Task 6: Frontend local status + badge

**Files:**
- Modify: `web/src/api/types.ts` (Service gains `image_local`)
- Modify: `web/src/components/CheckStatusIcon.tsx` (+ `local` case)
- Modify: `web/src/components/DashboardTable.tsx` (Local badge + exclude from tally)
- Test: `web/src/components/CheckStatusIcon.test.tsx` (create if absent), `web/src/components/DashboardTable.test.tsx`

**Interfaces:**
- Consumes: `image_local: boolean`, `check_status: "local"`.
- Produces: a `Local` badge on local rows; local rows excluded from update counts and the up-to-date tally.

- [ ] **Step 1: Write the failing tests**

`web/src/components/CheckStatusIcon.test.tsx` (create if it does not exist; follow the render helpers used by sibling component tests):

```tsx
import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { CheckStatusIcon } from "./CheckStatusIcon";

describe("CheckStatusIcon", () => {
  test("local renders the local icon distinct from not_found", () => {
    render(<CheckStatusIcon status="local" />);
    expect(screen.getByLabelText("Built locally")).toBeInTheDocument();
  });

  test("not_found keeps its own label", () => {
    render(<CheckStatusIcon status="not_found" />);
    expect(screen.getByLabelText("Image not in registry")).toBeInTheDocument();
  });
});
```

In `web/src/components/DashboardTable.test.tsx`, add a case: a service with `image_local: true` shows a "Local" badge and is not counted as "up to date". Follow the file's existing fixture/render helpers; core assertion:

```tsx
  test("a local image shows the Local badge", async () => {
    // render the table with one service: { ...base, image_local: true, check_status: "local" }
    expect(await screen.findByText("Local")).toBeInTheDocument();
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- CheckStatusIcon DashboardTable`
Expected: FAIL (`local` label missing; no "Local" badge).

- [ ] **Step 3: Implement**

`web/src/api/types.ts`: add `image_local: boolean;` to the `Service` type (next to `check_status`).

`web/src/components/CheckStatusIcon.tsx`: add a `local` entry to `STATUS_TEXT` and a `case "local"` to the switch. Replace the file body's map + switch with:

```tsx
import { AlertTriangle, CircleSlash, Wrench } from "lucide-react";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

const STATUS_TEXT: Record<string, string> = {
  rate_limited:
    "Registry rate limit hit while checking this image. dockbrr retries on the next scan; registry credentials raise the limit.",
  error:
    "Registry lookup failed (network or registry error). dockbrr retries on the next scan.",
  not_found:
    "Image not found in its registry. If this is a private image, add credentials in Settings, Registries.",
  local:
    "Built locally from a compose build: directive. There is no registry to check, so dockbrr does not track updates for it.",
};
```

Then in the switch, before the `default`:

```tsx
    case "local":
      icon = <Wrench aria-label="Built locally" className="h-3.5 w-3.5 text-muted-foreground" />;
      break;
```

(Keep the existing `rate_limited`/`error` cases and the `default` `CircleSlash` "Image not in registry" for `not_found`.)

`web/src/components/DashboardTable.tsx`:
- Render a muted badge on a local service row. Find where the service row renders its status/name cell and add, guarded by `service.image_local`:

```tsx
{service.image_local && (
  <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">Local</span>
)}
```

- Exclude local services from any "up to date" / update-count aggregation. Locate the reducer/counter that tallies services (search the file for the count of updates or up-to-date services) and add `!s.image_local` to its filter predicate. If the tally is computed like `services.filter((s) => !hasUpdate(s))`, change to `services.filter((s) => !s.image_local && !hasUpdate(s))`. (Read the file to find the exact aggregation; there is one counts block near the project header.)

- [ ] **Step 4: Run tests + typecheck**

Run: `cd web && npm test -- CheckStatusIcon DashboardTable && npm run typecheck`
Expected: PASS, 0 type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/api/types.ts web/src/components/CheckStatusIcon.tsx web/src/components/CheckStatusIcon.test.tsx web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): local image status icon, badge, and tally exclusion"
```

---

# PART A: Watchtower-style self-update (Tasks 7-13)

### Task 7: Docker primitives for the self-update swap

**Files:**
- Create: `internal/docker/selfupdate.go` (`ContainerImageRef`, `SpawnUpdater`, `SwapContainer`, parse helpers)
- Test: `internal/docker/selfupdate_test.go` (pure parse-helper tests)

**Interfaces:**
- Consumes: existing `Client.InspectStatus`, `ContainerStop`, `ContainerRemove`, `ContainerCreateFromInspect`, `ContainerStart`, `ContainerIDByName`, `ImagePull`.
- Produces:
  - `func (cl *Client) ContainerImageRef(ctx context.Context, id string) (string, error)`
  - `func (cl *Client) SpawnUpdater(ctx context.Context, image string, cmd []string, socketPath string) (string, error)`
  - `func (cl *Client) SwapContainer(ctx context.Context, targetID, newImage string, logf func(string)) error`
  - `updaterContainerName = "dockbrr-self-update"` (exported const `UpdaterContainerName`).

- [ ] **Step 1: Write the failing test**

Create `internal/docker/selfupdate_test.go`:

```go
package docker

import "testing"

func TestParseInspectNameAndImage(t *testing.T) {
	raw := `{"Name":"/dockbrr","Config":{"Image":"ghcr.io/yorah/dockbrr:1.1.0"}}`
	name, image, err := parseInspectNameImage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if name != "dockbrr" {
		t.Errorf("name = %q, want dockbrr (leading slash trimmed)", name)
	}
	if image != "ghcr.io/yorah/dockbrr:1.1.0" {
		t.Errorf("image = %q", image)
	}
}

func TestParseInspectNameImageEmpty(t *testing.T) {
	if _, _, err := parseInspectNameImage(""); err == nil {
		t.Fatal("expected error on empty inspect JSON")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/docker/ -run TestParseInspect`
Expected: FAIL (`parseInspectNameImage` undefined).

- [ ] **Step 3: Implement**

Create `internal/docker/selfupdate.go`:

```go
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

// UpdaterContainerName is the fixed name of the detached helper container that
// performs a self-update swap. A fixed name lets dockbrr clean up a leftover
// helper (from a failed swap) on its next boot.
const UpdaterContainerName = "dockbrr-self-update"

// ContainerImageRef returns a container's configured image reference (Config.Image).
// Read-only.
func (cl *Client) ContainerImageRef(ctx context.Context, id string) (string, error) {
	ct, err := cl.c.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("docker: inspect %s: %w", id, err)
	}
	if ct.Config == nil {
		return "", fmt.Errorf("docker: inspect %s: no config", id)
	}
	return ct.Config.Image, nil
}

// SpawnUpdater creates and starts a detached helper container that runs `image`
// with `cmd`, bind-mounting the Docker socket so the helper can drive the daemon.
// It is NOT auto-removed: a failed swap leaves the container in place so its logs
// survive for `docker logs`; a successful swap's leftover is pruned on the next
// dockbrr boot (removeLeftoverUpdater). Any prior container with the fixed name
// is removed first. Returns the new helper container id.
func (cl *Client) SpawnUpdater(ctx context.Context, image string, cmd []string, socketPath string) (string, error) {
	if id, ok, err := cl.ContainerIDByName(ctx, UpdaterContainerName); err != nil {
		return "", err
	} else if ok {
		_ = cl.c.ContainerRemove(ctx, id, dcontainer.RemoveOptions{Force: true})
	}
	cfg := &dcontainer.Config{Image: image, Cmd: cmd}
	host := &dcontainer.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: socketPath,
			Target: socketPath,
		}},
		RestartPolicy: dcontainer.RestartPolicy{Name: "no"},
	}
	resp, err := cl.c.ContainerCreate(ctx, cfg, host, nil, nil, UpdaterContainerName)
	if err != nil {
		return "", fmt.Errorf("docker: create updater: %w", err)
	}
	if err := cl.c.ContainerStart(ctx, resp.ID, dcontainer.StartOptions{}); err != nil {
		return "", fmt.Errorf("docker: start updater: %w", err)
	}
	return resp.ID, nil
}

// removeLeftoverUpdater best-effort removes a stopped leftover helper container
// from a prior self-update. Called on boot. A running helper (a swap still in
// flight) is left alone.
func (cl *Client) removeLeftoverUpdater(ctx context.Context) {
	id, ok, err := cl.ContainerIDByName(ctx, UpdaterContainerName)
	if err != nil || !ok {
		return
	}
	_ = cl.c.ContainerRemove(ctx, id, dcontainer.RemoveOptions{})
}

// RemoveLeftoverUpdater is the exported boot-cleanup entry point.
func (cl *Client) RemoveLeftoverUpdater(ctx context.Context) { cl.removeLeftoverUpdater(ctx) }

// SwapContainer replaces the target container with newImage, preserving its full
// config via ContainerCreateFromInspect. It is executed by the detached helper
// (the target is dockbrr itself, so the process calling this dies at the stop;
// the helper survives). On a create/start failure AFTER the old container was
// removed, it best-effort recreates the old container from the same inspect with
// its original image, so a failed swap lands back on a running old version.
func (cl *Client) SwapContainer(ctx context.Context, targetID, newImage string, logf func(string)) error {
	if logf == nil {
		logf = func(string) {}
	}
	st, err := cl.InspectStatus(ctx, targetID)
	if err != nil {
		return fmt.Errorf("inspect target: %w", err)
	}
	name, oldImage, err := parseInspectNameImage(st.RawJSON)
	if err != nil {
		return err
	}
	logf("stopping " + name)
	if err := cl.ContainerStop(ctx, targetID); err != nil {
		return fmt.Errorf("stop target: %w", err)
	}
	logf("removing " + name)
	if err := cl.ContainerRemove(ctx, targetID); err != nil {
		return fmt.Errorf("remove target: %w", err)
	}
	logf("creating " + name + " from " + newImage)
	newID, err := cl.ContainerCreateFromInspect(st.RawJSON, newImage, name)
	if err != nil {
		logf("create failed, rolling back to " + oldImage + ": " + err.Error())
		return rollback(ctx, cl, st.RawJSON, oldImage, name, logf, err)
	}
	if err := cl.ContainerStart(ctx, newID); err != nil {
		logf("start failed, rolling back to " + oldImage + ": " + err.Error())
		_ = cl.ContainerRemove(ctx, newID)
		return rollback(ctx, cl, st.RawJSON, oldImage, name, logf, err)
	}
	logf("started " + name + " (" + newID + ") on " + newImage)
	return nil
}

// rollback recreates the old container from the captured inspect and returns the
// original swap error wrapped. Best-effort: a rollback failure is logged.
func rollback(ctx context.Context, cl *Client, rawJSON, oldImage, name string, logf func(string), cause error) error {
	id, rerr := cl.ContainerCreateFromInspect(rawJSON, oldImage, name)
	if rerr != nil {
		logf("rollback create failed: " + rerr.Error())
		return fmt.Errorf("swap failed (%w) and rollback failed: %v", cause, rerr)
	}
	if rerr := cl.ContainerStart(ctx, id); rerr != nil {
		logf("rollback start failed: " + rerr.Error())
		return fmt.Errorf("swap failed (%w) and rollback start failed: %v", cause, rerr)
	}
	logf("rolled back to " + oldImage)
	return fmt.Errorf("swap failed, rolled back: %w", cause)
}

// parseInspectNameImage extracts the container name (leading slash trimmed) and
// its configured image from an inspect JSON blob. Pure.
func parseInspectNameImage(rawJSON string) (name, image string, err error) {
	if strings.TrimSpace(rawJSON) == "" {
		return "", "", errors.New("docker: empty inspect JSON")
	}
	var meta struct {
		Name   string
		Config struct{ Image string }
	}
	if err := json.Unmarshal([]byte(rawJSON), &meta); err != nil {
		return "", "", fmt.Errorf("docker: parse inspect: %w", err)
	}
	return strings.TrimPrefix(meta.Name, "/"), meta.Config.Image, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/docker/`
Expected: PASS (new parse tests + existing docker tests; `go vet ./internal/docker/` clean).

- [ ] **Step 5: Commit**

```bash
git add internal/docker/selfupdate.go internal/docker/selfupdate_test.go
git commit -m "feat(docker): self-update swap, updater spawn, and leftover cleanup"
```

---

### Task 8: `self-update-swap` subcommand entry point

**Files:**
- Modify: `cmd/dockbrr/main.go` (subcommand branch + `runSelfUpdateSwap`)
- Create: `cmd/dockbrr/selfupdate_swap.go` (the helper command implementation)

**Interfaces:**
- Consumes: `docker.New`, `Client.SwapContainer` (Task 7).
- Produces: `dockbrr self-update-swap --socket <path> --target <id> --image <ref>` runs the swap and exits.

- [ ] **Step 1: Write the failing test**

Add to a `cmd/dockbrr` test file (create `cmd/dockbrr/selfupdate_swap_test.go`):

```go
package main

import "testing"

func TestSelfUpdateSwapFlagsRequired(t *testing.T) {
	if err := runSelfUpdateSwap([]string{"--socket", "/x.sock", "--target", "abc"}); err == nil {
		t.Fatal("expected error when --image is missing")
	}
	if err := runSelfUpdateSwap([]string{"--image", "img", "--target", "abc"}); err == nil {
		t.Fatal("expected error when --socket is missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/dockbrr/ -run TestSelfUpdateSwapFlagsRequired`
Expected: FAIL (`runSelfUpdateSwap` undefined).

- [ ] **Step 3: Implement**

Create `cmd/dockbrr/selfupdate_swap.go`:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"dockbrr/internal/docker"
)

// swapStartDelay gives the parent dockbrr process a moment to mark its
// self_update job terminal and close its log streams before this helper stops
// it. Without it the helper could kill the parent mid-Finish.
const swapStartDelay = 2 * time.Second

// runSelfUpdateSwap is the detached helper entry point (`dockbrr self-update-swap`).
// It connects to the Docker socket and swaps the target container to a new image.
func runSelfUpdateSwap(args []string) error {
	fs := flag.NewFlagSet("self-update-swap", flag.ContinueOnError)
	socket := fs.String("socket", "", "Docker socket path")
	target := fs.String("target", "", "target container id to replace")
	image := fs.String("image", "", "new image reference")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *socket == "" || *target == "" || *image == "" {
		return errors.New("self-update-swap: --socket, --target and --image are all required")
	}
	dc, err := docker.New(*socket)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	logf := func(s string) { fmt.Fprintln(os.Stdout, "[self-update] "+s) }
	time.Sleep(swapStartDelay)
	logf("swapping " + *target + " -> " + *image)
	if err := dc.SwapContainer(context.Background(), *target, *image, logf); err != nil {
		logf("ERROR: " + err.Error())
		return err
	}
	logf("done")
	return nil
}
```

In `cmd/dockbrr/main.go` `main()`, add the branch right after the `--version` handling (after line 74, before `run(...)`):

```go
	if len(os.Args) > 1 && os.Args[1] == "self-update-swap" {
		if err := runSelfUpdateSwap(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/dockbrr/ -run TestSelfUpdateSwapFlagsRequired && go build ./cmd/dockbrr/`
Expected: PASS + builds.

- [ ] **Step 5: Commit**

```bash
git add cmd/dockbrr/selfupdate_swap.go cmd/dockbrr/selfupdate_swap_test.go cmd/dockbrr/main.go
git commit -m "feat(cmd): self-update-swap helper subcommand"
```

---

### Task 9: `SelfUpdater` job runner

**Files:**
- Create: `internal/job/selfupdate.go` (`SelfUpdater`, `SelfDocker` interface, `targetSelfImage`)
- Test: `internal/job/selfupdate_test.go`

**Interfaces:**
- Consumes: `store.Jobs.Finish`, `Emitter.Emit`, `selfupdate.Checker.Check`, docker methods from Task 7, `detect.SplitRef`.
- Produces:
  - `type SelfDocker interface { ContainerImageRef(...); ImagePull(...); SpawnUpdater(...) }`
  - `type SelfChecker interface { Check(ctx) (selfupdate.Result, error) }`
  - `func NewSelfUpdater(jobs *store.Jobs, emitter Emitter, dc SelfDocker, checker SelfChecker, selfID, socket string) *SelfUpdater`
  - `func (u *SelfUpdater) Handle(ctx context.Context, job store.Job)`
  - `func targetSelfImage(currentRef, latestTag string) string`

- [ ] **Step 1: Write the failing test**

Create `internal/job/selfupdate_test.go`:

```go
package job

import (
	"context"
	"errors"
	"testing"

	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

func TestTargetSelfImage(t *testing.T) {
	cases := []struct{ ref, latest, want string }{
		{"ghcr.io/yorah/dockbrr:latest", "v1.2.0", "ghcr.io/yorah/dockbrr:latest"}, // floating kept
		{"ghcr.io/yorah/dockbrr:1.1.0", "v1.2.0", "ghcr.io/yorah/dockbrr:1.2.0"},   // pinned swapped, v stripped
		{"ghcr.io/yorah/dockbrr", "v1.2.0", "ghcr.io/yorah/dockbrr"},               // untagged == floating, kept
	}
	for _, c := range cases {
		if got := targetSelfImage(c.ref, c.latest); got != c.want {
			t.Errorf("targetSelfImage(%q,%q) = %q, want %q", c.ref, c.latest, got, c.want)
		}
	}
}

type fakeSelfDocker struct {
	imageRef   string
	pulled     string
	pullErr    error
	spawnedCmd []string
	spawnErr   error
}

func (f *fakeSelfDocker) ContainerImageRef(ctx context.Context, id string) (string, error) {
	return f.imageRef, nil
}
func (f *fakeSelfDocker) ImagePull(ctx context.Context, ref string) error {
	f.pulled = ref
	return f.pullErr
}
func (f *fakeSelfDocker) SpawnUpdater(ctx context.Context, image string, cmd []string, socket string) (string, error) {
	f.spawnedCmd = cmd
	return "helper123", f.spawnErr
}

type fakeChecker struct {
	res selfupdate.Result
	err error
}

func (f fakeChecker) Check(ctx context.Context) (selfupdate.Result, error) { return f.res, f.err }

func TestSelfUpdaterHappyPath(t *testing.T) {
	jobs, emitter := newJobFixture(t) // helper: creates a *store.Jobs + an Emitter, enqueues one self_update job, returns it
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0"}
	ck := fakeChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs) // helper enqueues + claims a self_update job, returns store.Job
	u.Handle(context.Background(), j)

	if fd.pulled != "ghcr.io/yorah/dockbrr:1.2.0" {
		t.Errorf("pulled = %q, want ...:1.2.0", fd.pulled)
	}
	if len(fd.spawnedCmd) == 0 || fd.spawnedCmd[0] != "self-update-swap" {
		t.Errorf("helper cmd = %v", fd.spawnedCmd)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "success" {
		t.Errorf("job status = %q, want success", got.Status)
	}
}

func TestSelfUpdaterPullFailureKeepsRunning(t *testing.T) {
	jobs, emitter := newJobFixture(t)
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0", pullErr: errors.New("network down")}
	ck := fakeChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.spawnedCmd != nil {
		t.Errorf("helper spawned despite pull failure: %v", fd.spawnedCmd)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Errorf("job status = %q, want failed", got.Status)
	}
}

func TestSelfUpdaterNotInContainer(t *testing.T) {
	jobs, emitter := newJobFixture(t)
	fd := &fakeSelfDocker{}
	ck := fakeChecker{res: selfupdate.Result{UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "", "/var/run/docker.sock") // empty selfID

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.pulled != "" {
		t.Errorf("pulled despite not-in-container: %q", fd.pulled)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Errorf("job status = %q, want failed", got.Status)
	}
}
```

Two helpers keep the tests DRY. Read `internal/job/dispatch_test.go` and `internal/job/worker_test.go` for how they open a store and build a `*store.Jobs`, then define:

```go
type noopEmitter struct{}

func (noopEmitter) Emit(int64, string, string) {}

// newJobFixture opens a store and returns a *store.Jobs plus a no-op Emitter.
func newJobFixture(t *testing.T) (*store.Jobs, Emitter) {
	t.Helper()
	db := openTestStore(t) // use the store helper the job tests already use
	return store.NewJobs(db), noopEmitter{}
}

// enqueueSelfUpdate enqueues and claims one self_update job, returning it.
func enqueueSelfUpdate(t *testing.T, jobs *store.Jobs) store.Job {
	t.Helper()
	if _, err := jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	j, ok, err := jobs.ClaimNext()
	if err != nil || !ok {
		t.Fatalf("claim self_update job: ok=%v err=%v", ok, err)
	}
	return j
}
```

(If the job tests use a differently-named store opener, match it. `noopEmitter` may already exist in a sibling test file; reuse it rather than redeclaring.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run 'SelfUpdater|TargetSelfImage'`
Expected: FAIL (`NewSelfUpdater`, `targetSelfImage` undefined).

- [ ] **Step 3: Implement**

Create `internal/job/selfupdate.go`:

```go
package job

import (
	"context"
	"strings"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

// SelfDocker is the Docker surface the self-updater needs. *docker.Client
// satisfies it.
type SelfDocker interface {
	ContainerImageRef(ctx context.Context, id string) (string, error)
	ImagePull(ctx context.Context, ref string) error
	SpawnUpdater(ctx context.Context, image string, cmd []string, socketPath string) (string, error)
}

// SelfChecker reports whether a newer dockbrr release exists. *selfupdate.Checker
// satisfies it.
type SelfChecker interface {
	Check(ctx context.Context) (selfupdate.Result, error)
}

// SelfUpdater runs a self_update job: pull the new dockbrr image in-process (so a
// pull failure never touches the running container), then hand the actual
// container swap to a detached helper that outlives this process. See
// docs/dev/specs/2026-07-18-self-update-apply-and-local-images-design.md.
type SelfUpdater struct {
	jobs    *store.Jobs
	emitter Emitter
	docker  SelfDocker
	checker SelfChecker
	selfID  string // dockbrr's own container id ("" when not in a container)
	socket  string // Docker socket path, bind-mounted into the helper
}

// NewSelfUpdater wires a SelfUpdater.
func NewSelfUpdater(jobs *store.Jobs, emitter Emitter, dc SelfDocker, checker SelfChecker, selfID, socket string) *SelfUpdater {
	return &SelfUpdater{jobs: jobs, emitter: emitter, docker: dc, checker: checker, selfID: selfID, socket: socket}
}

// Handle executes the self_update job.
func (u *SelfUpdater) Handle(ctx context.Context, job store.Job) {
	emit := func(msg string) {
		if u.emitter != nil {
			u.emitter.Emit(job.ID, "system", msg)
		}
	}
	fail := func(msg string) {
		emit(msg)
		logger.Warnf("self-update: job %d failed: %s", job.ID, msg)
		_ = u.jobs.Finish(job.ID, "failed", nil, msg)
	}

	if u.selfID == "" {
		fail("self-update is only available when dockbrr runs in a container")
		return
	}
	res, err := u.checker.Check(ctx)
	if err != nil {
		fail("could not check for a dockbrr update: " + err.Error())
		return
	}
	if !res.UpdateAvailable {
		fail("no dockbrr update is available")
		return
	}
	currentRef, err := u.docker.ContainerImageRef(ctx, u.selfID)
	if err != nil {
		fail("could not resolve dockbrr's own image: " + err.Error())
		return
	}
	newImage := targetSelfImage(currentRef, res.Latest)
	emit("pulling " + newImage)
	if err := u.docker.ImagePull(ctx, newImage); err != nil {
		fail("pull " + newImage + " failed: " + err.Error())
		return
	}
	cmd := []string{"self-update-swap", "--socket", u.socket, "--target", u.selfID, "--image", newImage}
	if _, err := u.docker.SpawnUpdater(ctx, currentRef, cmd, u.socket); err != nil {
		fail("could not start the update helper: " + err.Error())
		return
	}
	emit("pulled " + newImage + "; restarting into the new version (dockbrr will be briefly unavailable)")
	logger.Infof("self-update: job %d spawned helper, restarting into %s", job.ID, newImage)
	_ = u.jobs.Finish(job.ID, "success", nil, "")
}

// targetSelfImage computes the image the self-update should move to. A floating
// tag (latest, or untagged) is kept as-is (a re-pull moves its digest); a pinned
// tag is swapped to latestTag with any leading "v" stripped to match the image
// tag convention (GoReleaser publishes "1.2.0", the release tag is "v1.2.0").
func targetSelfImage(currentRef, latestTag string) string {
	repo, tag := detect.SplitRef(currentRef)
	if tag == "" || tag == "latest" {
		return currentRef
	}
	norm := strings.TrimPrefix(latestTag, "v")
	if norm == "" {
		return currentRef
	}
	return repo + ":" + norm
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/job/ -run 'SelfUpdater|TargetSelfImage'` then `go test ./internal/job/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/job/selfupdate.go internal/job/selfupdate_test.go
git commit -m "feat(job): SelfUpdater runner (in-process pull + detached swap)"
```

---

### Task 10: Route `self_update` through the dispatcher

**Files:**
- Modify: `internal/job/dispatch.go` (`SetSelfUpdater`, `case "self_update"` in `Handle`)
- Test: `internal/job/dispatch_test.go`

**Interfaces:**
- Consumes: `SelfUpdater` (Task 9).
- Produces: `func (d *Dispatcher) SetSelfUpdater(u *SelfUpdater)`; `Handle` routes `self_update` to it (before the existing switch), failing the job cleanly when no updater is wired.

- [ ] **Step 1: Write the failing test**

Add to `internal/job/dispatch_test.go`:

```go
func TestDispatchSelfUpdateWithoutUpdaterFails(t *testing.T) {
	// A dispatcher with no SelfUpdater wired must fail a self_update job cleanly
	// (not panic, not route it to the compose applier). d.jobs must be populated
	// for the fallback to mark the job failed; SetSelfGuard wires it.
	db := openTestStore(t) // reuse the file's store helper
	jobs := store.NewJobs(db)
	services := store.NewServices(db)
	d := NewDispatcher(nil, nil, nil, nil)
	d.SetSelfGuard("abc123def456", services, jobs, noopEmitter{}) // populates d.jobs
	if _, err := jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	j, _, err := jobs.ClaimNext()
	if err != nil {
		t.Fatal(err)
	}
	d.Handle(context.Background(), j)
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
}
```

(Adapt `openTestStore`/`noopEmitter` to the existing helpers in the job test files. `SetSelfGuard` is what populates `d.jobs`, so the no-updater fallback in Step 3 can mark the job failed.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run TestDispatchSelfUpdate`
Expected: FAIL (self_update falls through to `d.applier.Handle` and nil-panics, or status not failed).

- [ ] **Step 3: Implement**

In `internal/job/dispatch.go`:

Add a field and setter:

```go
	selfUpdater *SelfUpdater
```

```go
// SetSelfUpdater wires the self-update runner. Call before the engine starts.
// Without it, self_update jobs fail cleanly.
func (d *Dispatcher) SetSelfUpdater(u *SelfUpdater) { d.selfUpdater = u }
```

In `Handle`, add the route as the first `case`, before `start/stop/...`:

```go
func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	if job.Type == "self_update" {
		if d.selfUpdater == nil {
			logger.Warnf("job: self_update (job %d) with no updater wired; failing", job.ID)
			if d.jobs != nil {
				_ = d.jobs.Finish(job.ID, "failed", nil, "self-update is not available (docker unreachable)")
			}
			return
		}
		d.selfUpdater.Handle(ctx, job)
		return
	}
	if d.refuseSelfTarget(job) {
		return
	}
	// ... existing switch unchanged ...
}
```

`self_update` is deliberately absent from `mutatingTypes`, so `refuseSelfTarget` would ignore it anyway; routing it first also means a self_update job with no service/project target never reaches the guard. `d.jobs` is populated by `SetSelfGuard`; in the no-updater fallback it may be nil (guard disabled on a host install), hence the nil check. In production the updater is always wired when Docker is up (Task 11), so the fallback only fires in the Docker-down window.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/job/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/job/dispatch.go internal/job/dispatch_test.go
git commit -m "feat(job): route self_update jobs to the SelfUpdater"
```

---

### Task 11: Wire the updater + boot cleanup in main

**Files:**
- Modify: `cmd/dockbrr/main.go` (`startDockerServices`: construct + set SelfUpdater, remove leftover helper)

**Interfaces:**
- Consumes: `NewSelfUpdater` (Task 9), `Dispatcher.SetSelfUpdater` (Task 10), `Client.RemoveLeftoverUpdater` (Task 7), `job.SelfContainerID`, `cfg.DockerSocket`, `selfUpdateChecker`.
- Produces: a wired self-update path whenever Docker is reachable.

- [ ] **Step 1 (no separate test): Implement the wiring**

In `cmd/dockbrr/main.go` `startDockerServices`, right after the dispatcher is built and the self-guard armed (after line 235, `dispatcher.SetSelfGuard(...)` block), add:

```go
		// Self-update: pull the new image in-process, then a detached helper swaps
		// this container. Wired only when Docker is reachable (here). Clean up any
		// leftover helper from a prior (possibly failed) self-update first.
		dc.RemoveLeftoverUpdater(ctx)
		if selfID := job.SelfContainerID(); selfID != "" {
			dispatcher.SetSelfUpdater(job.NewSelfUpdater(jobs, engine, dc, selfUpdateChecker, selfID, cfg.DockerSocket))
		}
```

`selfUpdateChecker`, `jobs`, `engine`, `cfg`, `dc` are all in scope (the checker and cfg via the enclosing `run` closure; `dc` is the function parameter). `engine` satisfies `job.Emitter`.

- [ ] **Step 2: Build + vet**

Run: `CGO_ENABLED=0 go build ./... && go vet ./...`
Expected: clean build, no vet errors.

- [ ] **Step 3: Full Go test**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/dockbrr/main.go
git commit -m "feat(cmd): wire self-updater and leftover-helper cleanup"
```

---

### Task 12: `POST /api/updates/self/apply` endpoint

**Files:**
- Modify: `internal/httpapi/selfupdate.go` (add `handleSelfUpdateApply`)
- Modify: `internal/httpapi/server.go` (route)
- Test: `internal/httpapi/selfupdate_test.go` (create if absent)

**Interfaces:**
- Consumes: `s.deps.Engine.Enqueue`, `s.deps.SelfUpdate.Check`, `job.SelfContainerID`.
- Produces: `POST /api/updates/self/apply` returns `{ "job_id": <id> }` (200) or a 409 with a message when preconditions fail.

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/selfupdate_test.go` (follow another handler test in the package for `Server` construction with an in-memory store + auth bypass). Assertions:

```go
func TestSelfUpdateApplyNoUpdate409(t *testing.T) {
	// SelfUpdate.Check reports update_available:false -> 409, no job enqueued.
	// (Construct a Server whose deps.SelfUpdate returns UpdateAvailable:false.)
	// POST /api/updates/self/apply -> expect 409.
}

func TestSelfUpdateApplyEnqueues(t *testing.T) {
	// update_available:true AND running "in a container" -> 200 with a job_id,
	// and a self_update job is present in the queue.
	// Note: SelfContainerID() reads os.Hostname(); to make the in-container
	// branch testable, guard the handler on an injectable self-id (see Step 3).
}
```

Because `job.SelfContainerID()` reads the real hostname, make the container check injectable: add a `SelfID string` to `Deps` (populated in main from `job.SelfContainerID()`), and have the handler treat `s.deps.SelfID == ""` as "not in a container". The test then sets `deps.SelfID = "abc123def456"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestSelfUpdateApply`
Expected: FAIL (route + handler missing).

- [ ] **Step 3: Implement**

In `internal/httpapi/server.go` `Deps`, add:

```go
	// SelfID is dockbrr's own container id ("" on a host install). Gates the
	// self-update apply endpoint; injected so it stays testable.
	SelfID string
```

Populate it in `cmd/dockbrr/main.go` where `deps` is built (in the `httpapi.Deps{...}` literal, near `SelfUpdate: selfUpdateChecker,`):

```go
		SelfID: job.SelfContainerID(),
```

In `internal/httpapi/selfupdate.go`, add the handler:

```go
// handleSelfUpdateApply enqueues a self_update job that pulls the latest dockbrr
// image and hands the container swap to a detached helper. Preconditions (no
// update available, or not running in a container) return 409 with a message and
// enqueue nothing.
func (s *Server) handleSelfUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfID == "" {
		writeJSONError(w, http.StatusConflict, errors.New("self-update is only available when dockbrr runs in a container"))
		return
	}
	if s.deps.SelfUpdate == nil {
		writeJSONError(w, http.StatusConflict, errors.New("self-update is unavailable"))
		return
	}
	res, err := s.deps.SelfUpdate.Check(r.Context())
	if err != nil || !res.UpdateAvailable {
		writeJSONError(w, http.StatusConflict, errors.New("no dockbrr update is available"))
		return
	}
	id, err := s.deps.Engine.Enqueue(store.Job{Type: "self_update", RequestedBy: "user"})
	if err != nil {
		writeInternalError(w, "enqueue self_update", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": id})
}
```

Add the imports `errors` and `dockbrr/internal/store` to `selfupdate.go` if not present.

In `server.go` `routes()`, inside the authed group next to the existing `r.Get("/api/updates/self", ...)`:

```go
		r.Post("/api/updates/self/apply", s.handleSelfUpdateApply)
```

(Confirm `s.deps.Engine` has an `Enqueue(store.Job) (int64, error)` method exposed via the `Deps.Engine` type; `*job.Engine` does. If `Deps.Engine` is a narrower interface, add `Enqueue` to it.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/httpapi/ && CGO_ENABLED=0 go build ./...`
Expected: PASS + build.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/selfupdate.go internal/httpapi/selfupdate_test.go internal/httpapi/server.go cmd/dockbrr/main.go
git commit -m "feat(httpapi): POST /api/updates/self/apply enqueues a self_update job"
```

---

### Task 13: Frontend "Update now" + panel titles

**Files:**
- Modify: `web/src/hooks/queries.ts` (`useApplySelfUpdate`)
- Modify: `web/src/components/layout/UpdateNotice.tsx` (Update now button)
- Modify: `web/src/components/ApplyPanel.tsx` (self_update titles)
- Test: `web/src/components/layout/UpdateNotice.test.tsx`

**Interfaces:**
- Consumes: `POST /api/updates/self/apply` (Task 12).
- Produces: an "Update now" action on the update notice; the live panel labels `self_update`.

- [ ] **Step 1: Write the failing test**

In `web/src/components/layout/UpdateNotice.test.tsx`, add a case (mock `useSelfUpdate` with `update_available:true`, and an msw POST handler for `/api/updates/self/apply`):

```tsx
  test("Update now posts to the apply endpoint", async () => {
    let posted = false;
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json({ current: "1.1.0", latest: "v1.2.0", html_url: "https://x", update_available: true })),
      http.post("/api/updates/self/apply", () => { posted = true; return HttpResponse.json({ job_id: 5 }); }),
    );
    renderApp("/"); // or render UpdateNotice within the app harness the file already uses
    const btn = await screen.findByRole("button", { name: /update now/i });
    await userEvent.click(btn);
    await waitFor(() => expect(posted).toBe(true));
  });
```

(Match the file's existing render + msw setup.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- UpdateNotice`
Expected: FAIL (no "Update now" button).

- [ ] **Step 3: Implement**

`web/src/hooks/queries.ts`: add a mutation (follow the file's existing `apiFetch` + `useMutation` pattern, including the CSRF header used by other mutations):

```ts
export function useApplySelfUpdate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<{ job_id: number }>("/api/updates/self/apply", { method: "POST" }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.jobs });
    },
  });
}
```

`web/src/components/layout/UpdateNotice.tsx`: add an "Update now" button beside "View Release". Wire it to `useApplySelfUpdate`; while the mutation is pending show a disabled "Updating..." label (the browser connection drops when the swap restarts dockbrr, which is expected):

```tsx
const apply = useApplySelfUpdate();
// ...
<button
  type="button"
  disabled={apply.isPending}
  onClick={() => apply.mutate()}
  className="..." // match the notice's existing button styling
>
  {apply.isPending ? "Updating..." : "Update now"}
</button>
```

(Keep the existing "View Release" link. If the notice has a collapsed icon-only variant, leave it linking to the release page only; the "Update now" action lives in the expanded card.)

`web/src/components/ApplyPanel.tsx`: add `self_update` to `TITLES` and `SUCCESS_LABELS`:

```ts
const TITLES: Record<string, string> = {
  apply: "Applying update",
  rollback: "Rolling back",
  start: "Starting",
  stop: "Stopping",
  restart: "Restarting",
  remove: "Removing",
  self_update: "Updating dockbrr",
};
```

```ts
const SUCCESS_LABELS: Record<string, string> = {
  apply: "Applied",
  rollback: "Rolled back",
  start: "Started",
  stop: "Stopped",
  restart: "Restarted",
  remove: "Removed",
  self_update: "Update started",
};
```

- [ ] **Step 4: Run tests + typecheck + build**

Run: `cd web && npm test -- UpdateNotice ApplyPanel && npm run typecheck && npm run build`
Expected: PASS, 0 type errors, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/queries.ts web/src/components/layout/UpdateNotice.tsx web/src/components/layout/UpdateNotice.test.tsx web/src/components/ApplyPanel.tsx
git commit -m "feat(web): Update now action and self_update panel titles"
```

---

### Task 14: Regression coverage for compose-label clone

**Files:**
- Modify: `internal/docker/recreate_test.go`

**Interfaces:**
- Consumes: `createArgsFromInspect` (existing).
- Produces: a test proving `com.docker.compose.*` labels survive the clone (compose-deployed dockbrr keeps its compose identity across a self-update swap).

- [ ] **Step 1: Write the test** (a regression guard; it should pass immediately since labels ride in `Config`)

Add to `internal/docker/recreate_test.go`:

```go
func TestCreateArgsFromInspectPreservesComposeLabels(t *testing.T) {
	raw := `{"Name":"/dockbrr","Config":{"Image":"old:1","Labels":{"com.docker.compose.project":"stack","com.docker.compose.service":"dockbrr"}},"HostConfig":{},"NetworkSettings":{"Networks":{}}}`
	cfg, _, _, _, err := createArgsFromInspect(raw, "new:2")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "new:2" {
		t.Errorf("image = %q, want new:2", cfg.Image)
	}
	if cfg.Labels["com.docker.compose.project"] != "stack" {
		t.Errorf("compose project label dropped: %v", cfg.Labels)
	}
	if cfg.Labels["com.docker.compose.service"] != "dockbrr" {
		t.Errorf("compose service label dropped: %v", cfg.Labels)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/docker/ -run TestCreateArgsFromInspectPreservesComposeLabels`
Expected: PASS (labels are part of `Config`, carried verbatim). If it fails, that is a real clone bug to fix before shipping.

- [ ] **Step 3: Commit**

```bash
git add internal/docker/recreate_test.go
git commit -m "test(docker): compose labels survive the self-update clone"
```

---

## Final verification

- [ ] **Full suite**

Run: `mise run check` (go vet + go test + web vitest).
Expected: all green. Then `cd web && npm run build` for the type/backstop build, and `CGO_ENABLED=0 go build ./...` to confirm the static binary still builds.

- [ ] **Manual smoke (self-update, in-container)**

Deploy the built image as a container, publish a newer tag (or point at `:latest` and re-pull), click "Update now" in the sidebar notice. Verify: the job pulls, the helper container `dockbrr-self-update` appears, dockbrr restarts on the new image with the same ports/volumes/env, and the leftover helper is gone after the new boot. Confirm the pre-existing self-guard still refuses a plain apply/restart on dockbrr's own service.

- [ ] **Manual smoke (local image)**

Add a compose project with a `build:` service. Verify its dashboard row shows the grey "Local" badge, `check_status` is `local` (tooltip "Built locally..."), it is excluded from the update/up-to-date counts, and no registry probe fires for it in the logs.
