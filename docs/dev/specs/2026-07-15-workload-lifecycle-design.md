# Workload lifecycle: start/stop/restart, remove, logs, standalone apply

## Problem

dockbrr monitors compose projects and standalone containers and applies image
updates, but it cannot act on a workload's lifecycle. Two concrete gaps:

1. **No lifecycle control.** A user who sees a stuck or stopped service on the
   dashboard cannot start, stop, restart, or view its logs without dropping to a
   shell. The Loose grouping surfaced the orphan stopped standalone containers,
   but there is no way to clean them up (remove) from the UI.
2. **Standalone apply silently fails.** The detector records updates for
   standalone containers (e.g. `backrest`), and the UI offers an Apply button,
   but apply routes through the compose runner. A standalone container has no
   compose files, so `compose.Parse` returns `no config files` and the job dies
   at precheck. Standalone updates are effectively informational only, while the
   UI implies they are actionable.

This spec adds lifecycle control for the workloads dockbrr already monitors, and
makes standalone apply actually work, using a new docker-SDK mutation channel
that runs through the existing Job Engine.

## Goals

- Start / stop / restart any monitored service (loose or compose), correct for
  the one hard dependency case (shared namespace).
- Remove loose (standalone) containers, stopped-only, with bulk multi-select.
- View a bounded tail of a container's logs (read-only), with room to add live
  follow later.
- Make apply (and rollback) work for standalone containers via SDK recreate,
  reusing the existing snapshot schema.

## Non-goals

- Compose-project-scope lifecycle (stop/start the whole stack in dependency
  order). Lifecycle is per-service.
- Host-wide `docker container prune`. Removal is scoped to dockbrr's own loose
  containers, never a blind host sweep.
- `exec` / interactive terminal.
- Live-follow (SSE) logs. The tail API is shaped so follow can be added without
  a breaking change.
- Parsing `stop_grace_period`; the daemon default stop timeout is used.
- Transitive namespace chains and the reverse namespace case (starting a
  container whose own namespace provider is down). Direct dependents only,
  matching apply today.
- Container removal for compose services (that is `compose down`, project-scoped
  and riskier). Remove is loose-only.

## Identity note

Lifecycle plus standalone apply moves dockbrr from a pure "compose update
manager" toward a "workload manager for the containers it monitors." The line
held here: dockbrr manages the workloads it already discovers and displays. It
does not browse, create-from-scratch, or prune arbitrary containers. Every
mutation is scoped to a service/container already in dockbrr's view.

## Design

### 1. Docker SDK surface (`internal/docker`)

The `docker.Client` already wraps the full SDK client and is read-only today
(Collect / InspectStatus / Ping / ServerVersion). Add:

- Mutations (called ONLY by the Job Engine worker, never the API):
  - `ContainerStart(ctx, id) error`
  - `ContainerStop(ctx, id) error` (daemon default timeout)
  - `ContainerRestart(ctx, id) error`
  - `ContainerRemove(ctx, id) error` (no force; the caller guarantees stopped)
  - `ContainerRename(ctx, id, newName) error`
  - `ImagePull(ctx, ref) error` (blocks until the pull stream drains)
  - `ContainerCreateFromInspect(ctx, inspectJSON string, newImage, name string) (id string, err error)`
    and `ContainerConnectNetwork(ctx, id, network string, endpoint) error` for
    the recreate path (see section 6).
- Read-only (callable from the API handler):
  - `ContainerLogsTail(ctx, id string, tail int) (string, error)`

These are typed SDK calls with no shell, preserving the spirit of invariant 6
(no user-built command strings). They are a NEW mutation channel alongside the
compose-argv runner; both remain the only ways to mutate Docker, both driven by
the Job Engine.

### 2. Job Engine: new kinds + mechanism

Add job kinds `start`, `stop`, `restart`, `remove`, scope `service`. The apply
and rollback kinds stay but branch on project kind (section 6).

- Lifecycle jobs run through the existing **per-project keyed mutex**, so a
  stop/start/restart/remove serializes with apply/rollback for the same project.
  No lifecycle op can race an in-flight apply.
- A new worker path (`Lifecycle`, separate from the compose `Applier`) handles
  the four lifecycle kinds. On completion it triggers rediscovery (reuse the
  applier's rediscoverer) so container state, health, and the "gone" transition
  refresh promptly.

**Snapshot carve-out.** Invariant 3 ("snapshot precedes every mutation") is
about image apply/rollback: a snapshot captures the pre-apply image identity and
container inspect so an update can be rolled back. Lifecycle ops change no image
and are reversible by their opposite (start/stop) or by re-running (restart), so
they take **no snapshot**. Remove targets a stopped orphan with no meaningful
rollback. This is documented as a scope refinement of invariant 3, not a
violation: the invariant binds image mutation (apply), which still always
snapshots first (including standalone apply, section 6).

### 3. Start / stop / restart (namespace-aware)

Per-service, both loose and compose. The container effect is identical to
`docker start|stop|restart` on the service's container ids; dockbrr does not
replicate `depends_on` ordering (it is an up-time hint, soft at runtime, and not
modeled anywhere in dockbrr). The one hard case is shared namespace.

**Resolving the ordered set.** For the target service, the job computes its
**namespace dependents** (other services whose `network_mode` / `ipc` / `pid` is
`service:<target>`, which cannot exist without the target's namespace):

- Compose service with parseable files: `composer.Parse(WorkingDir, ConfigFiles)`
  then `NamespaceDependents(svc.Name)` (the existing, tested helper) gives
  dependent service names; their container ids come from the store.
- Loose container, or a compose project whose files are missing/unparseable
  (unmanaged): the set is just the target's own container(s). Graceful fallback,
  matches how docker itself behaves.

**Order (target T, namespace dependents D):**

- **stop:** stop D (each container), then T. Dependents cannot survive losing
  T's namespace, so they go down first.
- **start:** start T, then D. Dependents can only join T's namespace once T runs.
- **restart:** orchestrated-stop (D..., T) then orchestrated-start (T, D...). D
  must re-join T's fresh namespace after T restarts.

A service may have multiple container ids (replicas merged in discovery); the op
applies to all of them. Direct dependents only (one level), matching apply.

### 4. Remove (loose, stopped-only, bulk)

- Backend guard (not just UI): the target project must be `kind == "standalone"`
  AND every target container must be stopped. A running or non-standalone target
  is rejected before any mutation.
- Single remove: one `remove` job -> `ContainerRemove` the container(s).
- Bulk: the Loose group supports multi-select. Selecting N stopped loose
  containers and confirming enqueues **one `remove` job per selected service**
  (independent per-project mutexes let them proceed in parallel where possible;
  a single failure does not block the others). The confirm dialog lists every
  container that will be removed by name before proceeding.
- After removal, discovery reconciles the now-absent container: the service goes
  `gone`, then ages out per the existing lifecycle. No new "gone" handling
  needed.

### 5. Logs (tail now, follow later)

- Read-only `GET /api/services/:id/logs?tail=N` (default 500, capped at a sane
  max e.g. 2000). Returns combined stdout+stderr as text. Served directly from
  the API handler via `ContainerLogsTail`, which is read-only, so invariant 2
  ("UI/API never touches Docker directly") holds: that invariant forbids direct
  MUTATION from the UI/API, and this is a read. It is dockbrr's first API->docker
  read path; the docker client is injected into the httpapi deps for it.
- Multi-container services: tail the first container id (note in UI which one).
- The endpoint is shaped so a later `?follow=1` can upgrade to an SSE stream
  (like the job live-log panel) without changing the tail contract.

### 6. Standalone apply + rollback (SDK recreate)

The `apply` and `rollback` job handlers branch on `project.Kind`:

- `compose` -> existing `Applier` (compose pull + up), unchanged.
- `standalone` -> new `StandaloneApplier` (SDK recreate).

The standalone path reuses the **existing snapshot schema unchanged**:
`PrevContainerInspect` already stores the full `ContainerInspect` response
(`json.Marshal` of the entire InspectResponse: Config + HostConfig +
NetworkSettings), and `PrevRepo` / `PrevDigest` / `PrevImageID` store the old
image identity. `ComposeFileHash` / `ComposeBlob` are empty for standalone.

**Apply flow (StandaloneApplier):**

1. **Precheck (no mutation).** Load service, project, latest open update.
   Re-resolve the target ref and verify the resolved digest equals
   `upd.ToDigest`; if it moved, mark the update `superseded` and fail (reuse the
   existing precheck logic). Confirm the current container exists via inspect;
   if gone, fail.
2. **Snapshot (must succeed first).** Capture `InspectStatus(id).RawJSON` plus
   `PrevRepo` / `PrevDigest` / `PrevImageID` (the `snapshot()` shape, minus
   compose fields). This enables rollback.
3. **Pull-before-create (invariant 4).** `ImagePull` the target ref
   (`repo:tag` for a floating tag, `repo@digest` for a digest pin).
4. **Recreate.**
   - `ContainerStop` the old container.
   - `ContainerRename` old to `<name>-dockbrr-old` to free the name.
   - Reconstruct create args from the snapshot inspect JSON: `container.Config`
     with `Image` set to the new ref, `HostConfig` verbatim, and
     `NetworkingConfig` from `NetworkSettings.Networks`.
   - `ContainerCreate` with the original name.
   - Connect any additional networks beyond the first via `NetworkConnect`
     (ContainerCreate attaches one endpoint reliably; extra user networks are
     connected after create).
   - `ContainerStart` the new container.
5. **Health-gate the NEW container id (invariant 4).** Poll the recreated
   container for running/healthy (reuse the apply health-gate semantics, which
   already poll recreated ids, never pre-apply ids).
6. **Finalize.**
   - Success: `ContainerRemove` the old (`-dockbrr-old`) container; update the
     service row (new container id, new digest); emit applied event.
   - Failure at any mutation step: `ContainerRemove` the new container (if
     created), `ContainerRename` old back to the original name, `ContainerStart`
     old. This restores the pre-apply state in place. Mark the job failed.

**Rollback job (standalone).** Recreate from the snapshot's
`PrevContainerInspect` with the old `PrevRepo@PrevDigest` image, via the same
recreate helper (stop current -> rename -> create-from-snapshot-inspect with old
image -> start -> health-gate -> remove the superseded container). Since apply
only changed the image, recreating the snapshot config with the old image
restores the prior state.

**Config fidelity (the primary implementation risk).** The recreate helper must
reproduce the container identically except for the image. Fields copied verbatim
from the inspect response and explicitly exercised in tests:

- Environment, command/entrypoint, working dir, user, labels.
- Port bindings and exposed ports (`HostConfig.PortBindings`, `Config.ExposedPorts`).
- Volumes and mounts: `HostConfig.Binds`, `HostConfig.Mounts`, and anonymous
  volumes (`Config.Volumes`) preserved by reusing the same volume references, so
  data is not orphaned.
- Networks: every entry in `NetworkSettings.Networks` including aliases; the
  first attached at create, the rest connected after.
- Restart policy (`HostConfig.RestartPolicy`), cap-add/drop, devices, sysctls,
  ulimits, log config, healthcheck (`Config.Healthcheck`).
- `network_mode` / `ipc` / `pid` (including `container:<id>` forms) copied as-is;
  note that a loose container using `--network container:X` is recreated as the
  single container without orchestrating X (loose namespace deps are not modeled
  without a compose file). Documented limitation.

**Consequences.** The Apply button becomes functional for standalone, so the
earlier "hide the always-failing Apply button" bug is resolved by making it
work rather than hiding it. No standalone apply preview in the MVP (the compose
preview reuses compose specs; standalone shows a static "pull + recreate"
description). No compose write-back for standalone (no file).

### 7. API

- `POST /api/services/:id/lifecycle` body `{action: "start"|"stop"|"restart"}`
  -> enqueues the corresponding job, returns `{job_id}` (like apply, so the
  live-log panel can open on it). CSRF required.
- `POST /api/services/:id/remove` -> loose+stopped guard, enqueues a `remove`
  job, returns `{job_id}`. CSRF required.
- `GET /api/services/:id/logs?tail=N` -> read-only text tail. No CSRF (GET).
- Apply/rollback endpoints are unchanged; the standalone branch is internal to
  the worker.

### 8. UI (`DashboardTable`)

- Per-service action menu (extend the existing `ActionsCell`), state-aware:
  - `Start` when the service is stopped.
  - `Stop` and `Restart` when running.
  - `Logs` always (opens a logs drawer).
  - Apply stays where it is; it now works for standalone too.
- Loose group: a `Remove` action on each stopped loose row, plus multi-select on
  the group with a bulk `Remove` that opens a confirm dialog listing the
  containers.
- Logs drawer: a new drawer (mirroring `ChangelogDrawer`) showing the tail with
  a manual Refresh button. Follow mode is a later addition.
- Confirmations: Remove only (destructive, irreversible). Start / stop / restart
  have none (reversible).
- Mutations use the existing CSRF header + `credentials: include` + 401->auth
  gate conventions.

## Safety invariants impact

- **Invariant 2 (UI/API never mutate Docker directly; all mutation via the Job
  Engine).** Preserved. Lifecycle and standalone apply mutate only inside the
  Job Engine worker. The one new API->docker call is logs, which is read-only.
- **Invariant 3 (snapshot precedes every mutation).** Refined: binds image
  mutation (apply), which still always snapshots first, now including standalone
  apply. Lifecycle ops (start/stop/restart/remove) are non-image and take no
  snapshot, documented above.
- **Invariant 4 (pull-before-up; health-gate the recreated ids).** Honored by
  standalone apply: pull precedes create, and the health gate polls the new
  container id.
- **Invariant 5 (per-project keyed mutex).** Lifecycle jobs use it, serializing
  with apply/rollback per project.
- **Invariant 6 (compose verbs whitelisted, argv, no shell).** Unchanged for the
  compose path. The new SDK channel uses typed SDK calls, no shell, no
  user-built command strings, consistent with the invariant's intent.

## Testing

- `docker` package: unit tests for the recreate helper (inspect JSON ->
  create args) covering the fidelity fields (env, ports, binds, mounts,
  anonymous volumes, multiple networks with aliases, restart policy, labels,
  healthcheck, network_mode/ipc/pid). Mutation methods tested against a fake or
  with narrow interface seams; the pure inspect->create mapping is the primary
  unit under test (mirroring how `containerStatusFrom` is tested today).
- `job` package (Lifecycle worker): start/stop/restart order for a target with
  namespace dependents (stop = D then T; start = T then D; restart = both);
  loose/unmanaged fallback to target-only; remove guard rejects running or
  non-standalone; per-project mutex serializes lifecycle with apply.
- `job` package (StandaloneApplier): precheck supersede on digest drift; snapshot
  written before mutation; pull-before-create ordering; health-gate on the new
  id; success removes old; failure path restores the old container
  (remove-new, rename-back, start-old); rollback recreates from snapshot inspect
  with the old image.
- `httpapi`: lifecycle endpoint enqueues the right job kind and returns a job id;
  remove endpoint enforces the loose+stopped guard (403/409 on a running or
  compose target); logs endpoint returns a bounded tail and rejects an
  out-of-range `tail`; CSRF enforced on the POSTs, not the GET.
- `web`: action menu is state-aware (Start vs Stop/Restart by state); Logs drawer
  fetches and renders the tail with Refresh; Loose-group multi-select + bulk
  Remove opens a confirm dialog listing containers and fires the removes;
  standalone Apply is offered and drives the apply mutation.
- Backend regression: full `CGO_ENABLED=0 go build ./... && go vet ./... &&
  go test ./...`. Frontend: `./node_modules/.bin/tsc -b --noEmit && npm test`.
