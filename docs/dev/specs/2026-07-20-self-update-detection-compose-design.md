# Self-update detection under Compose, and routing Apply-on-self

Date: 2026-07-20
Status: Approved

## Problem

Running dockbrr via Docker Compose (`ghcr.io/yorah/dockbrr:latest`, the recommended
deployment), clicking **Apply** on an available update for dockbrr's own project
recreates the dockbrr container mid-job and breaks dockbrr. The intended protections
do not fire.

dockbrr already has two protections, both gated on `job.SelfContainerID()` returning a
non-empty id (`cmd/dockbrr/main.go:240`, `:249`, `:355`):

1. **Self-guard** (`internal/job/dispatch.go:refuseSelfTarget`): refuses any mutating job
   (`apply`/`rollback`/`start`/`stop`/`restart`/`remove`) whose target containers include
   dockbrr's own container.
2. **Self-updater** (`internal/job/selfupdate.go`): the watchtower-style path. Pulls the
   new dockbrr image in-process, then hands the container swap to a detached helper that
   outlives the process. Reached via the `self_update` job type.

`SelfContainerID()` (`dispatch.go:157`) is the only self-detection, and it recognizes just
one convention:

```go
h, err := os.Hostname()
if err != nil || len(h) != 12 { return "" }   // must be exactly 12 hex chars
```

That is the `docker run` default (hostname = short container id). Under Docker Compose the
container hostname is the service name (e.g. `dockbrr`), which is not 12 hex chars, so
`SelfContainerID()` returns `""`. That silently disables **both** protections:

- Guard never armed -> Apply on dockbrr's own project runs the normal compose applier
  -> `docker compose up -d` recreates the dockbrr container -> kills dockbrr mid-job.
- Self-updater never wired, and `Deps.SelfID == ""` -> `handleSelfUpdateApply` returns 409,
  so the safe swap path is unreachable.

## Goals

1. Detect dockbrr's own container id reliably regardless of how it was started (compose,
   `docker run`, custom hostname). Re-arms both existing protections.
2. When the user clicks Apply on dockbrr's own update, transparently run the self-updater
   instead of refusing, with the special nature made explicit to the user (a pre-click
   confirmation dialog and a clear job-log line).
3. Clicking "Check for updates" re-shows the sidebar update notification even if it was
   previously dismissed for the current latest version.

## Non-goals

- No auto-update. Self-update stays user-triggered.
- No change to the compose applier, snapshot/rollback, or job engine serialization.
- No rollback of a self-update (the swap is forward-only; a bad version is fixed from the
  host, unchanged from today).

## Design

### A. Robust self-detection (`internal/job`)

Replace the hostname-only check with a layered probe. Order, first hit wins:

1. **`/proc/self/mountinfo`**: Docker bind-mounts `/etc/hostname`, `/etc/hosts`, and
   `/etc/resolv.conf` from `/var/lib/docker/containers/<id>/`. Each mountinfo line carries
   that host path in its "root" field. Extract the first 64-hex id matching
   `/containers/<64hex>/`.
2. **`/proc/self/cgroup`**: cgroup v1 lines contain `.../docker/<64hex>` (or
   `docker-<64hex>.scope`). Extract the 64-hex id. Covers hosts where mountinfo lacks the
   pattern but cgroup still names the container.
3. **`os.Hostname()` 12-hex**: the current check, preserved so the `docker run` default and
   any deployment that sets hostname to the short id keeps working.

Returns `""` only when none match (genuine host install -> protections stay disabled, as
today).

Structure for testability: pure functions taking file *content* (a string), each returning
the id or `""`:

- `parseContainerIDFromMountinfo(content string) string`
- `parseContainerIDFromCgroup(content string) string`
- `parseContainerIDFromHostname(hostname string) string` (the existing 12-hex logic)

`SelfContainerID()` becomes the thin wrapper that reads the real files / calls `os.Hostname()`
and tries each in order. File reads that error are treated as "no match" and fall through.

`targetsSelf` (`dispatch.go:120`) already prefix-matches stored container ids against
`selfID` in both directions, so a 64-hex id compared to a stored short or full id just works.
No change needed there.

### B. Route Apply-on-self to the self-updater (`internal/httpapi`)

**Self-target detection at the API layer.** Add a helper on `Server` that reports whether a
resolved service is dockbrr's own container:

```go
func (s *Server) serviceIsSelf(svc store.Service) bool {
    // false when Deps.SelfID == "" (host install) — same prefix-match as dispatch.targetsSelf
}
```

**`handleApply` (`updates.go:73`)**: after resolving update -> service -> project and the
existing `Unmanaged` / `gone` guards, check `serviceIsSelf(svc)`. If true, do not enqueue a
normal `apply`. Instead run the self_update preconditions and enqueue a `self_update` job.

**Shared precondition helper.** Extract the body of `handleSelfUpdateApply`
(`selfupdate.go:48`) — in-container (`SelfID != ""`), checker succeeds, update available,
single-flight via `Jobs.ActiveByType("self_update")` — into a helper both handlers call:

```go
// returns (jobID, httpStatus, err); status 0 means "enqueued", non-zero is the code to write
func (s *Server) enqueueSelfUpdate(ctx context.Context) (int64, int, error)
```

`handleSelfUpdateApply` becomes a thin wrapper. In `handleApply`, on `serviceIsSelf` the
response is `{"job_id": <id>, "self_update": true}` (200). A normal apply keeps its current
`{"job_id": <id>}` (202). The `self_update: true` flag lets the UI confirm what happened, but
is not the primary UI trigger (see C).

**Defense in depth.** `dispatcher.refuseSelfTarget` is unchanged. Any mutating job that
targets self and is *not* an API-routed self_update — a rollback, restart, stop, remove, or an
`apply` that somehow reaches the engine — is still refused with the existing "manage from the
host" message. dockbrr's own project is a single-container project, so a routed apply targets
only self; the guard's apply branch is now a fallback, not the normal path.

### C. Make the special case explicit (UI)

**Expose self-identity up front.** To show a dialog *before* the POST, the UI must know a
given update targets dockbrr itself without first calling apply. Add `is_self` (bool) to the
update list payload (`handleListUpdates`, `updates.go:29`), computed from `serviceIsSelf`.
`web/src/api/types.ts` gains `is_self` on the update type.

**Pre-click confirmation dialog.** Two trigger points share one dialog component:

- The per-project/service **Apply** button (`ApplyPanel.tsx`) when the update's `is_self` is
  true.
- The sidebar **Update now** button (`UpdateNotice.tsx`) — always a self-update by nature.

Dialog copy (plain, states the mechanism and the consequence):

> **Update dockbrr itself?**
> This updates dockbrr using its built-in self-update: it pulls the new image and hands the
> container swap to a short-lived helper. dockbrr will restart and this page will briefly
> disconnect, then reconnect on the new version.
> [Cancel] [Update dockbrr]

Confirm -> the existing self-update call (`POST /api/updates/self/apply`, or the routed
`handleApply` for the ApplyPanel path). Cancel -> nothing enqueued. Non-self applies are
unaffected: no dialog, current behavior.

**Job-log clarity.** The `self_update` job's first emitted line states plainly it is a
self-update image swap driven by a detached helper, distinct from a normal compose apply
(extends the existing emits in `selfupdate.go:Handle`).

### D. "Check for updates" re-shows a dismissed notice (UI)

Today `UpdateNotice` reads `localStorage["dockbrr_dismissed_update"]` (= the dismissed
latest tag) once into a `useState` snapshot and hides when `dismissed === data.latest`. A
manual check cannot un-hide it: the state is component-local and not reactive to an external
clear.

Changes:

1. **Reactive dismissal hook.** Replace the `useState`/`localStorage.getItem` snapshot with a
   small `useSyncExternalStore`-backed hook (`useDismissedUpdate`) reading the same
   localStorage key. Its setter writes the key and dispatches a custom
   `dockbrr:dismiss-changed` window event; the hook subscribes to that event (and `storage`)
   so every mount re-renders when the value changes. Dismiss behavior and the per-version key
   are unchanged.
2. **Clear on manual check.** `useCheckForUpdates.onSuccess` (`mutations.ts:206`), in addition
   to `setQueryData`, removes `dockbrr_dismissed_update` and dispatches
   `dockbrr:dismiss-changed`. The manual check is an explicit request to see current status,
   so a previously dismissed notice reappears immediately, no reload. Clearing is
   unconditional (harmless when no update is available — the notice renders nothing).

## Data flow

Apply-on-self (the fixed path):

```
UI Apply/Update-now click
  -> is_self true -> confirmation dialog -> confirm
  -> POST self-update apply (or handleApply routes to self_update)
  -> enqueueSelfUpdate: preconditions + single-flight -> Enqueue{Type: self_update}
  -> SelfUpdater.Handle: pull new image in-process -> SpawnUpdater(detached helper)
  -> helper swaps the container; dockbrr restarts; page reconnects on new version
```

Guard (unchanged, now actually armed under compose):

```
Any mutating job targeting self that is NOT a self_update
  -> dispatcher.refuseSelfTarget -> job marked failed, "manage from the host" message
```

## Error handling

- Detection file reads error / absent -> treated as no-match, fall through to next probe;
  `""` only when all miss. No panics, no hard dependency on `/proc`.
- Self-update preconditions unchanged: not-in-container / checker error / no-update ->
  409 with distinct messages; a doomed job is never enqueued. Single-flight returns the
  existing job id.
- `serviceIsSelf` false whenever `SelfID == ""` (host install) -> apply behaves exactly as
  today.
- UI dialog cancel enqueues nothing.

## Testing

Go:

- `parseContainerIDFromMountinfo` / `parseContainerIDFromCgroup` /
  `parseContainerIDFromHostname`: table tests with real sample content — compose mountinfo
  (64-hex present), cgroup v1 line, 12-hex hostname, service-name hostname (`dockbrr` ->
  `""`), empty / malformed -> `""`.
- `handleApply` self-routing: `Deps.SelfID` set + a service whose `ContainerIDs` prefix-match
  -> enqueues `self_update`, response `self_update: true`; non-self service -> normal `apply`.
- `enqueueSelfUpdate` shared helper: preconditions and single-flight (reuse existing
  `selfupdate_test.go` coverage, retargeted at the helper).
- `handleListUpdates`: `is_self` reflects `serviceIsSelf`.
- Existing `dispatch_test.go` guard tests unchanged and still green.

Web (vitest):

- `UpdateNotice`: dismiss hides; a `dockbrr:dismiss-changed` clear re-shows without remount.
- `useCheckForUpdates`: success clears the dismiss key and dispatches the event.
- Confirmation dialog: shown only when `is_self`; confirm triggers the mutation, cancel does
  not.

## Safety invariants (CLAUDE.md)

- Static binary preserved: `/proc` reads are stdlib file reads, CGO-free.
- All Docker mutation still flows through the Job Engine; the API only enqueues.
- Per-project keyed mutex and single-flight self_update unchanged.
- No new shell-built command strings; compose verbs untouched.
- Frontend: CSRF on the mutating apply call unchanged; dialog adds no new network surface.
