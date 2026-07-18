# Self-update apply + local-image awareness design

Two independent features bundled in one branch.

- **Part A**: in-app watchtower-style self-update. dockbrr replaces its own
  container with a newer image, via a detached helper.
- **Part B**: local-image awareness. Images built from a compose `build:`
  directive are classified `local` (not an error), skip registry probes, and read
  as intentional on the dashboard.

This supersedes the "No in-app self-update / auto-download" non-goal in
`2026-07-17-self-update-notification-design.md`: the notification feature stands,
this adds the apply action on top of it.

---

# Part A: Watchtower-style self-update

## Goal

From the existing "update available" notice, let the operator update dockbrr in
place with one click. dockbrr pulls the new image and swaps its own container,
preserving all runtime config (ports, volumes, env, networks, restart policy,
labels).

## The self-replacement problem

A container cannot cleanly replace itself: the process orchestrating the
`stop -> remove -> create -> start` dies at the `stop`, leaving the swap
half-done. The existing self-guard (`internal/job/dispatch.go`) exists precisely
because an in-process apply/restart on dockbrr's own container would kill it
mid-job, leave the job stuck `running`, and `ResumeInterrupted` would re-run it
against ourselves on every boot.

watchtower solves this with a **detached helper** container that outlives the
container being replaced. We do the same, splitting the risky-but-recoverable
work (network pull) from the unrecoverable work (the swap):

- The **pull** happens in-process, where a failure is fully recoverable (dockbrr
  keeps running, job fails cleanly, zero downtime).
- The **swap** happens in a detached helper that survives dockbrr's death.

## Architecture

### Helper = dockbrr's own image, different command

No second image is shipped (preserves the single-static-binary invariant). The
helper container runs the **same image dockbrr currently runs**, with a
subcommand instead of the server:

```
dockbrr self-update-swap --target <container-id> --image <new-ref>
```

`cmd/dockbrr/main.go` branches on `os.Args[1] == "self-update-swap"` **before**
any server/store/flag setup and runs the helper path, which needs only the Docker
client (socket) and a logger.

The helper runs the *current* (old) image because the swap logic already lives in
that binary; it does not need the new image to execute, only to `create` the new
dockbrr from.

### `self_update` job type

New job type `self_update`, enqueued by a new endpoint (below). It is **not** in
`dispatch.go`'s `mutatingTypes` set, so `refuseSelfTarget` ignores it. This is the
single sanctioned self-target path; the guard still blocks accidental
apply/rollback/start/stop/restart/remove on dockbrr's own container.

Routing: `Dispatcher.Handle` gains a `case "self_update":` that dispatches to a
new `SelfUpdater` runner (below), before the existing switch arms.

### Runner: `internal/job/selfupdate.go` (`SelfUpdater`)

Runs inside the live dockbrr process when the `self_update` job is claimed:

1. **Resolve `newImage`.** Read dockbrr's own container image ref (via
   `SelfContainerID()` + inspect, or the injected self-image accessor). Compute
   the target ref:
   - Floating tag (`:latest`, or no tag): keep the ref as-is (re-pull moves the
     floating tag to its newest digest).
   - Pinned semver tag (`:1.1.0`): swap the tag to the self-update checker's
     latest release tag, normalized to the image tag convention (GoReleaser
     publishes both `1.2.0` and `latest`; a `v`-prefixed release tag maps to the
     `v`-less image tag).
2. **Guard rails.** If not running in a container (`SelfContainerID() == ""`), or
   the checker reports no update available, fail the job with a clear message
   ("self-update is only available when dockbrr runs in a container"). No helper
   is spawned.
3. **Pull in-process.** `ImagePull(ctx, newImage)`. On failure: `Finish(failed,
   ...)` with the pull error; dockbrr keeps running; **no downtime**. This is
   where all network risk is contained.
4. **Spawn the detached helper.** Create + start a helper container:
   - image: dockbrr's current image ref,
   - command: `["self-update-swap", "--target", <selfShortId>, "--image", newImage]`,
   - mounts: the Docker socket (cloned from our own inspect, or an explicit
     `/var/run/docker.sock` bind matching the configured socket path),
   - `AutoRemove: true`, **no restart policy**, detached.
5. **Log + finish.** Emit a job log line ("pulled `<newImage>`, restarting into
   the new version; dockbrr will be briefly unavailable") and `Finish(success)`.
   The helper then stops the current dockbrr shortly after; the job row is already
   terminal, so no stuck-`running` state and nothing for `ResumeInterrupted` to
   pick up.

### Helper: `self-update-swap` path

Detached, survives the parent's death. Minimal dependencies (Docker client +
logger). Steps:

1. **Inspect the target** by id (parent still alive at this point) -> raw inspect
   JSON + container name. Abort (exit non-zero, log) if inspect fails; parent
   untouched.
2. **Swap:** `stop(target)` -> `remove(target)` ->
   `ContainerCreateFromInspect(inspectJSON, newImage, name)` -> `start(newId)`.
   `ContainerCreateFromInspect` (already in `internal/docker/recreate.go`) clones
   Config/HostConfig/networks and reattaches named + anonymous volumes, swapping
   `Config.Image` to `newImage` and reusing the original name.
3. **Rollback on swap failure.** If `create` or `start` fails *after* the old
   container was removed, best-effort recreate with the **old** image
   (`Config.Image` from the captured inspect) from the same JSON and start it, so
   a swap failure lands back on a running old dockbrr rather than nothing. Log the
   full sequence to a file under the data dir (`<data>/logs/self-update.log`) for
   post-mortem, since the helper's stdout vanishes with `--rm`.
4. **Exit.** `AutoRemove` cleans up the helper container.

Downtime is the `stop`-old to `start`-new window (seconds). Acceptable for a
single-user tool.

### Deploy-mode coverage

Both plain `docker run` and compose deployments work: the clone preserves the full
Config incl. `com.docker.compose.*` labels, so a compose-deployed dockbrr keeps
its compose identity (a later `docker compose up` by the operator sees the same
container, exactly as watchtower behaves for compose-managed containers).

## HTTP endpoint

- `POST /api/updates/self/apply` (authed, CSRF-guarded like other mutations).
  Enqueues a `self_update` job. Returns `{ "job_id": <id> }`.
- Preconditions returning `409`/`400` with a message (no job enqueued):
  - not running in a container (`SelfContainerID() == ""`),
  - `SelfUpdate.Check` reports `update_available: false`.
- Wire into `server.go` `routes()` inside the authed group, and add the `Jobs`
  enqueue dependency (already present) plus the self-image accessor to `Deps`.

## Frontend

- `web/src/hooks/queries.ts`: `useApplySelfUpdate` mutation ->
  `POST /api/updates/self/apply`, invalidates `keys.jobs` on success and opens the
  live job panel (reuse `ApplyPanel` via the jobs screen / a returned `job_id`).
- `web/src/components/layout/UpdateNotice.tsx`: add an **Update now** button beside
  the existing **View Release** link. Clicking it fires the mutation and surfaces
  the job (toast + the update job appears in Jobs with the live `ApplyPanel`,
  which already titles `self_update`... add a title entry, see below).
  - Disable + spinner while the mutation is in flight; the browser will drop the
    connection when the swap restarts dockbrr, which is expected. A short
    "dockbrr is restarting..." state, then the SPA reconnects on the new version.
- `web/src/components/ApplyPanel.tsx`: add `self_update` entries to `TITLES`
  ("Updating dockbrr") and `SUCCESS_LABELS` ("Update started") so the live panel
  labels the job.

## Testing (Part A)

### Go

- `SelfUpdater` runner (`internal/job`): fake Docker client + self-image accessor.
  - pull failure -> job `failed`, no helper spawned, "dockbrr keeps running".
  - not-in-container -> job `failed` with the guard message, no pull, no helper.
  - happy path -> pull called with computed `newImage`, helper container created
    with the right command/image/socket-mount, job `success`.
  - target-ref computation matrix: floating tag kept; pinned tag swapped to latest;
    `v`-prefix normalization.
- Helper swap sequence: unit-test the pure order/rollback logic against a fake
  client (inspect -> pull-skipped -> stop -> remove -> create-from-inspect ->
  start; and the create-fails -> rollback-with-old-image branch).
- `createArgsFromInspect` already covered in `recreate_test.go`; add a case
  asserting `com.docker.compose.*` labels survive the clone.

### Vitest

- `UpdateNotice`: **Update now** present when `update_available`; click fires the
  POST (msw) and shows the restarting state; hidden/disabled when no update.

## Non-goals (Part A)

- No automatic/unattended self-update on a schedule. Operator-initiated only.
- No multi-node / swarm awareness.
- No preserving in-flight jobs across the restart beyond what persistence already
  gives (the queue is durable; a job mid-run at swap time is subject to normal
  `ResumeInterrupted` on the new version's boot).

---

# Part B: Local-image awareness

## Goal

An image built locally from a compose `build:` directive has no registry to check.
Today it surfaces as `not_found` (amber, "Image not in registry"), the same as a
typo'd or private ref that *should* resolve. Split them: `local` is expected and
intentional; `not_found` is a problem to investigate.

## Signal: the compose `build:` directive

The compose `build:` directive is the authoritative "this image is built here, not
pulled" marker. It is the only reliable local signal; a bare `not_found` on a
standalone container cannot be proven local (could be a typo), so those stay
`not_found`.

## Backend

### 1. Surface `build` from the parser

`internal/compose/parse.go`: add `Build bool` to `compose.Service`, set from the
compose-go `svc.Build != nil`. (`types.ServiceConfig.Build` is a
`*types.BuildConfig`; non-nil means a build section is present.)

### 2. Persist per-service `image_local`

- New `services.image_local` boolean column (store migration, default `0`).
- **Discovery** (`internal/discovery`) sets it when reconciling a compose project:
  for each discovered service, `image_local = (parsed build directive present)`.
  Re-derived every discovery cycle from the compose files, so it self-heals if a
  `build:` is added or removed. Standalone / non-compose services stay `false`.
- Add the field to `store.Service` and the `Services` scan/update paths.

### 3. Skip probing local images

`internal/scan` / `internal/detect`: a service with `image_local == true`
short-circuits before the network probe. Instead of resolving, it records
`check_status = "local"` for the service's `(repo, tag)` in `image_remote_state`
(status `"local"`, `resolved_at = now`) and returns `(nil, nil)`. The periodic
scan therefore never hits the registry for a local image.

Manual per-service check (`CheckServiceFresh`) may still force one probe attempt
(it invalidates the cached state), but because `image_local` is re-asserted from
the compose `build:` directive, the classification returns to `local`; a forced
probe never permanently demotes a build service to `not_found`. Concretely:
`CheckServiceFresh` re-reads the service, and if `image_local` is set it re-marks
`local` rather than resolving. (Wire the skip at the same layer that already
branches on `svc.ImageRef == "" || svc.CurrentDigest == ""`, so both the periodic
and fresh paths honor it.)

### 4. API

- `serviceDTO` (`internal/httpapi/projects.go`) gains `image_local bool`
  (`json:"image_local"`), copied from `store.Service`.
- `check_status` carries the new `"local"` value straight through the existing
  `image_remote_state` -> DTO mapping (no special-casing needed once discovery
  upserts `local`), and `image_local` is the authoritative flag the UI keys off.

## Frontend

- `web/src/components/CheckStatusIcon.tsx`: add a `local` case, distinct from
  `not_found`:
  - `local`: grey/muted icon (e.g. `Wrench` or `HardDrive`), tooltip "Built
    locally, no registry to track."
  - `not_found` keeps its amber `CircleSlash` "Image not in registry" wording.
- `web/src/components/DashboardTable.tsx`:
  - Render a muted **Local** badge/pill on the service row when `image_local`.
  - Exclude local services from the "N up to date" tally and any update-count
    aggregation (they have no update state; they must not read as "up to date" nor
    as an error).
- `web/src/api/types.ts`: `Service` gains `image_local: boolean`.

## Testing (Part B)

### Go

- `compose.Parse`: a service with a `build:` section -> `Build == true`; a plain
  `image:` service -> `false`.
- Discovery: reconciling a compose project with a build service persists
  `image_local = true`; removing the `build:` on the next cycle clears it.
- Detect/scan: `image_local` service -> no resolver call, `image_remote_state`
  upserted `status = "local"`, returns `(nil, nil)`. Periodic scan makes zero
  network calls for it. A forced `CheckServiceFresh` re-asserts `local`, not
  `not_found`.
- `projects.go`: DTO carries `image_local` and `check_status = "local"`.

### Vitest

- `CheckStatusIcon`: `local` renders the local icon + tooltip copy, distinct from
  `not_found`.
- `DashboardTable`: a service with `image_local` shows the Local badge and is
  excluded from the up-to-date tally / update counts.

## Non-goals (Part B)

- No detection of locally-built images without a compose `build:` directive (e.g.
  `docker build` + `docker run` standalone); those remain `not_found`. The build
  directive is the only reliable signal.
- No per-image "track a specific registry instead" override.

---

# Files touched (both parts)

Part A:
- `cmd/dockbrr/main.go` (subcommand branch)
- `internal/job/selfupdate.go` (new: `SelfUpdater` runner)
- `internal/job/selfupdate_test.go` (new)
- `internal/job/dispatch.go` (`self_update` routing; guard exemption already
  holds via `mutatingTypes`)
- `internal/job/swap.go` or similar (new: helper swap sequence + rollback)
- `internal/httpapi/selfupdate.go` (apply handler) + `server.go` (route, Deps)
- `internal/docker/recreate_test.go` (compose-label clone case)
- `web/src/hooks/queries.ts`, `web/src/api/types.ts`
- `web/src/components/layout/UpdateNotice.tsx` (+ test)
- `web/src/components/ApplyPanel.tsx` (titles)

Part B:
- `internal/compose/parse.go` (+ `parse_test.go`)
- `internal/discovery/discovery.go`
- `internal/store` (migration + `Service.ImageLocal` + scan/update)
- `internal/scan/scan.go` / `internal/detect/detect.go` (skip local)
- `internal/httpapi/projects.go` (DTO field)
- `web/src/components/CheckStatusIcon.tsx` (+ test)
- `web/src/components/DashboardTable.tsx` (+ test)
- `web/src/api/types.ts`
