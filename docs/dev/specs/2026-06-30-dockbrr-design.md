# dockbrr: Architecture & System Design

**Status:** Draft for review
**Date:** 2026-06-30
**Scope:** Architecture and system design only. No implementation.

A self-hosted system for monitoring and managing updates of Docker workloads: Docker Compose projects (primary) and standalone containers, with a fast, table-driven UI. Detects available image updates, shows changelogs, and lets the user review and manually apply updates via real `docker compose pull` + `docker compose up`. Never auto-updates unless explicitly enabled per project/service.

---

## 1. Goals / Non-goals

### Goals
- Monitor running containers and compose projects on a single Docker host.
- Detect available image updates from registries (tag + digest where possible).
- Surface updates in a fast, table-driven UI with changelog/release-notes links.
- Let the user review an update and manually trigger it; execute compose updates safely and controllably.
- Track previous state for rollback awareness (rollback action present; automation deferred).
- UI-first configuration; single-binary friendly; SQLite-backed.

### Non-goals (v1)
- Multi-host orchestration (designed-for, not built).
- Automated rollback (snapshot + manual one-click rollback only).
- Kubernetes/Podman/Swarm providers (extensible seams only).
- Notifications (Discord/Telegram/etc.): future.
- GitOps/declarative config as the source of truth (mitigated by settings export/import).

---

## 2. Resolved design decisions

| Decision | Choice | Rationale |
|---|---|---|
| **Host topology** | Single host core, with a `Host` abstraction seam | Keeps v1 simple. Multi-host becomes additive (Host rows + agent Executor) without touching the domain. |
| **Compose execution** | **Option A**: filesystem access to project dirs + real `docker compose` | Faithful to the user's own source of truth: preserves `.env`, overrides, bind mounts, profiles, dependency ordering. Enables clean rollback by re-pinning prior digest. Rejected: label-reconstruction (drift, weaker safety) and per-host agent (redundant on single host). |
| **Compose write engine** | Shell out to `docker compose` for writes (pull/up/down) | Byte-identical to a hand-run command. Lowest reimplementation risk. |
| **Compose read engine** | `compose-spec/compose-go` library for parsing/preview | Zero side effects; powers UI service/image/digest preview. |
| **Project path discovery** | Read compose's own container labels (`com.docker.compose.project.config_files`, `working_dir`) | dockbrr auto-learns where each stack lives; minimal mount config. |
| **Workload intake** | Auto-discover via Docker labels + manual add (UI) | Group running containers by `com.docker.compose.project`; standalone containers listed individually; user can also register a compose path in the UI. |
| **UI auth** | Single user, secure session cookie (argon2id, CSRF) | Smallest self-hosted footprint. `users` table allows later growth. |
| **Configuration** | **UI/DB-first**; flags/env for bootstrap only | Most config editable in UI; only the irreducible startup set lives in env/flags. |
| **Registry posture** | **Anonymous-first, credentials-on-demand** | Public registries (GHCR/Quay/registry.k8s.io) need no user creds. Creds prompted only on `401`. |
| **Stream transport** | SSE | Simple, single-binary friendly; sufficient for log/event streaming. |
| **Standalone containers** | Modeled as a `kind=standalone` project with one synthetic service | One update path for everything. |
| **Deployment** | Docker Compose (recommended) · native binary + systemd (secondary) · reverse proxy for TLS | Familiar self-hosted install menu. Compose image bundles the docker CLI + compose plugin; native install sidesteps path remapping. |

---

## 3. Component architecture & boundaries

Single static Go binary. The SPA is embedded via `embed.FS` and served by the same process.

```
                       dockbrr (one static Go binary)
  Browser ──HTTP/cookie──▶ ┌──────────────────────────────────────────┐
                           │ Web UI (React SPA, embed.FS)              │
                           │            │                              │
                           │       API layer (REST + SSE)              │
                           │   ┌────────┼─────────┬──────────┐         │
                           │   ▼        ▼         ▼          ▼         │
                           │ Inventory Scheduler Registry  Changelog   │
                           │ /Discovery (poll)   Client    Resolver    │
                           │   │        │         │          │         │
                           │   │        ▼         ▼          │         │
                           │   │     Update Detector ◀────────┘         │
                           │   │        │                              │
                           │   └────────┼────────▶ Job Engine ─────────┼──▶ docker compose / Docker API
                           │            ▼              │                │      (project dirs + socket)
                           │        Store (SQLite, single writer, WAL) │
                           └──────────────────────────────────────────┘
                                  │                         │
                            Docker socket            Registries (Hub/GHCR/OCI v2)
                                                     Changelog sources (GitHub, OCI labels)
```

### Safety boundary (the core invariant)
- The **detection path is strictly read-only**: Docker inspect + registry GET only.
- **Only the Job Engine mutates Docker.**
- The UI never touches Docker directly. Every action becomes a persisted job.
- The registry client performs network reads only.

### Components

| Component | Purpose | Interface (conceptual) | Depends on |
|---|---|---|---|
| **API layer** | Serve SPA, auth/session, translate UI actions into domain calls + jobs, stream logs/events | REST + SSE | all services, store |
| **Inventory / Discovery** | Enumerate containers via socket; group by compose labels into Projects + Services; track standalone; reconcile on timer + Docker events | `Sync()`, `List()` | Docker socket, store |
| **Scheduler** | Per-project/service check intervals + manual triggers; enqueue check jobs | `Tick()`, `Trigger(id)` | store, job engine |
| **Registry client** | Resolve remote tag→digest, manifest, OCI labels; Hub/GHCR/OCI v2; anonymous-first auth, rate-limit handling, cache | `Resolve(ref)` | creds (optional), network |
| **Update detector** | Compare current vs remote digest (platform-aware); compute version delta + severity; write `updates` | `Detect(service)` | inventory, registry, store |
| **Changelog resolver** | Fetch changelog text/link via fallback chain; cache | `Resolve(update)` | registry labels, GitHub API, store |
| **Job Engine / Executor** | Persisted queue + worker pool; per-project lock; run check/apply/rollback/sync; stream logs; snapshot state pre-mutation | `Enqueue(job)`, `Stream(id)` | socket, compose CLI, FS, store |
| **Store** | Persist all state; single writer, WAL | repository methods | SQLite file |
| **Web UI** | Fast, table-driven dashboard |, | API |

---

## 4. Data model

```
hosts 1─* projects 1─* services 1─* updates
                         services 1─* jobs 1─* job_logs
                         services 1─* state_snapshots
                         services 1─* service_events (history)
images (observed identities)   image_remote_state (tag→digest cache)
registry_credentials   settings   users
```

| Table | Key fields & notes |
|---|---|
| **hosts** | id, name, type(`local`/tcp/ssh/agent), socket_addr. *v1 = single `local` row; present so multi-host is additive.* |
| **projects** | id, host_id, **kind**(`compose`/`standalone`), name, working_dir, config_files(json), source(`discovered`/`manual`), **auto_update_enabled**(bool, default **false**), update_policy(json: scope, prune, timeout), last_synced_at |
| **services** | id, project_id, name, container_ids(json), image_ref(repo:tag), **current_digest**, current_image_id, **pinned**(bool, digest pinned in compose), state, healthcheck(bool), auto_update_enabled(override), updated_at |
| **images** | id, repo, tag, digest, media_type, os/arch, size, built_at, **labels(json)** (OCI annotations), source_url, revision, first_seen, last_seen. unique(repo,digest) |
| **image_remote_state** | repo, tag, remote_digest, resolved_at, manifest_labels(json), status(`ok`/`rate_limited`/`error`). Detection cache. |
| **updates** | id, service_id, from_digest, to_digest, from_version, to_version, tag, **severity**(major/minor/patch/digest-only), changelog_url, changelog_text(cached), detected_at, **status**(available/dismissed/applied/failed/superseded). unique(service_id,to_digest) |
| **jobs** | id, type(check/apply/rollback/sync), project_id, service_id, status(queued/running/success/failed/canceled), scope(service/project), requested_by(user/scheduler), created/started/finished_at, exit_code, error |
| **job_logs** | id, job_id, ts, stream(stdout/stderr/system), line |
| **state_snapshots** | id, service_id, job_id, prev_repo, **prev_digest**, prev_image_id, **prev_container_inspect(json)**, compose_file_hash, compose_blob(nullable), created_at. *Captured pre-mutation = rollback awareness.* |
| **service_events** | id, service_id, kind(detected/apply_started/succeeded/failed/rolled_back/dismissed), ref_job_id, from_digest, to_digest, message, created_at. *Powers per-service history.* |
| **registry_credentials** | id, registry_host, username, secret(**encrypted at rest**). Optional; only when a registry requires auth. |
| **settings** | typed k/v: default poll interval, concurrency, timeouts, github_token(encrypted), session_secret, air_gap_mode, prune defaults. UI-editable. |
| **users** | id, username, **password_hash**(argon2id). Single user v1; table allows growth. |

---

## 5. Update detection strategy

Image identity = `repo:tag@sha256:digest`. Per service:

1. **Determine tracked ref**: from compose `image:` (source of truth) or container `Config.Image`/labels. If compose pins `@sha256:`, mark the service `pinned`.
2. **Resolve remote digest** via registry client, GET manifest with OCI-index `Accept`; **platform-resolve** the manifest list to the host architecture so the compared digest is apples-to-apples with the running container's `RepoDigest`. Cache with TTL; respect registry rate-limit headers and anonymous tokens.
3. **Compare** running `RepoDigest` (platform-resolved) vs remote digest for the same tag. Differ → **update available**. Digest comparison is the always-reliable signal and catches a re-pushed `latest`.
4. **Compute version delta** (display + severity): parse semver from tags; query `/tags/list` for newer semver tags; or read `org.opencontainers.image.version` label. A moving tag (`latest`) with no derivable version → `digest-only`. Severity = semver bump class.
5. **Persist**: write/update the `updates` row; emit a `detected` event; mark a prior update `superseded` if a newer remote digest arrives.

### Edge handling
- **Multi-arch** → always platform-resolve before comparing.
- **Private registry 401** → use stored creds + token auth; persistent failure → mark image_remote_state `error`, surface in UI, **never crash the detection loop**.
- **Local / never-pushed image** → mark `unmonitorable`.
- **Pinned service** → still resolve the tag to *inform* ("newer digest exists (pinned)") but do not flag an apply unless the user opts in, because applying would rewrite the pinned compose file.
- **Docker Hub anonymous rate limit** → cache + backoff; surface a per-registry status badge so a limited image is visible rather than a silent stall.

---

## 6. Changelog retrieval: sources & fallbacks

Goal: per update, a changelog text or link. First hit wins; always keep at least a link.

1. **OCI labels / annotations** on the new image (already fetched by the registry client): `org.opencontainers.image.source` → repo URL; `.url`, `.documentation`, `.version`, `.revision`; legacy `org.label-schema.*`.
2. **GitHub/GitLab Releases** (when source known + versions parsed): fetch release for the tag, or notes between from→to. Handle `v`-prefix variants. Optional **GitHub token** (settings) for API rate limits: used for changelog only, never for pulls.
3. **Repo CHANGELOG fallback**: fetch `CHANGELOG.md`/`CHANGES`/`HISTORY.md` at the tag/default branch → link (best-effort).
4. **Registry-native**: Docker Hub `full_description`, GHCR/Quay description → text/link.
5. **Fallback link**: registry web page for the tag, or `documentation`/`url` label.
6. **None** → mark `unavailable`; still show the labels we have (version/revision/built_at).

Resolved text + URL cached on the `updates` row. Markdown sanitized before render. **Air-gap mode** → labels only, no network egress.

**GHCR synergy:** GHCR images commonly carry `org.opencontainers.image.source` → GitHub → Releases resolves directly. The optional GitHub token sharpens both source-label changelogs and Releases API limits.

---

## 7. Job execution & safety

Apply pipeline (scope = service | project, user-chosen). Steps run in order; any precheck failure aborts **before** any mutation.

1. **Acquire per-project lock** (mutex). Busy → queue. Guarantees one in-flight job per stack.
2. **Precheck**: compose files present and parseable (compose-go); engine reachable; **re-resolve remote digest**. If it moved off the intended `to_digest`, mark `superseded` and abort.
3. **Snapshot prior state** → `state_snapshots`: current repo + digest + image_id per affected service, container inspect JSON, compose file hash (+ optional blob). *No mutation ever happens without this.*
4. **Pull**: `docker compose pull <scope>`, streaming logs to `job_logs`. **On failure → abort. No `up` ran. The stack is untouched, still running the old image.**
5. **Apply**: `docker compose up -d <scope>` (`--no-deps` for single-service scope to avoid cascading restarts; full project for project scope). Compose recreates only changed containers.
6. **Health gate**: poll affected containers up to `timeout`: container running AND (if a healthcheck is defined) `healthy`, not in a restart loop or exited non-zero. Pass → mark `applied`, write `succeeded` history, close the update. **Fail → mark `apply_failed` and surface a one-click rollback** from the snapshot (re-pin prior digest, `up`). v1 = manual rollback; auto-rollback deferred.
7. **Release lock.** Optional prune (default off).

### Auto-update
Only when the project **and** service both have `auto_update_enabled` (default **OFF** on both). Same pipeline (health-gated, snapshotted) inside a per-project window. Pinned services are excluded from auto-apply.

### Failure modes & mitigations

| Failure | Cause | Mitigation |
|---|---|---|
| Broken stack post-update | bad image / config drift | pull-before-up (a failed pull never touches the running stack) + health gate + snapshot rollback |
| Partial multi-service update | one service fails mid-project | service scope by default; lock prevents interleave; failed service stops the pipeline, others left at prior state |
| Concurrent corruption | two jobs on the same project | per-project mutex + single SQLite writer |
| Compose file moved/missing | discovered container, file gone | precheck parse; missing → mark project `unmanaged`, disable apply, warn |
| Registry rate-limit / outage | Hub limits, network | cache + backoff + ratelimit headers; per-image isolation; surfaced, not fatal |
| Wrong-arch comparison | multi-arch manifest list | always platform-resolve |
| Pinned digest clobbered | apply rewrites pinned intent | pinned excluded from auto; explicit re-pin opt-in |
| Secret / env loss | API-recreate path | avoided, real compose against real files (Option A) |
| Socket = root authority | dockbrr drives Docker | UI auth; **only whitelisted compose verbs** (pull/up/down/ps/config); never arbitrary shell; optional socket-proxy |
| DB corruption | crash mid-write | WAL + single writer; jobs resumable; snapshot precedes mutate |
| Disk runaway | pulls accumulate | optional post-success prune (off by default); report disk |

### Safety invariants
- Detection is read-only; only the Job Engine mutates Docker.
- No mutation without a prior state snapshot.
- A pull must fully succeed before any `up`.
- One in-flight job per project (lock).
- Auto-update default OFF; opt-in per project **and** service; always health-gated.
- Only whitelisted compose verbs are executed; UI input never becomes shell.

---

## 8. UI

- **Dashboard table**: rows = services under collapsible project headers, plus standalone. Columns: name (project/service) · current image (repo:tag + short digest) · latest · status badge (Up-to-date / Update available / Pinned / Error / Updating) · version delta (severity color) · changelog link · last checked · actions. Filters (only-updates, by project, by status), search, dense and keyboard-friendly.
- **Review Update** → drawer: from→to (tag + digest), severity, rendered changelog + external link, affected containers, **command preview** (`compose pull/up`), scope toggle (service/project), pinned warnings. Buttons: Apply, Dismiss.
- **Apply Update** → creates an apply job; **live log stream (SSE)** of compose output; health-gate progress; success/fail result; rollback button on failure.
- **Service detail / History**: event timeline (detected/applied/failed/rolled-back/dismissed), past digests, per-job logs, current snapshot for rollback.
- **Settings**: registries + creds (optional), GitHub token, intervals, concurrency, per-project/service auto-update toggles, password, settings export/import.

---

## 9. Configuration model (UI-first)

- **UI/DB is primary** for all operational config: registries + optional creds, GitHub token, poll intervals, concurrency, timeouts, auto-update toggles, manual project intake, changelog/air-gap toggle, retention/prune, password.
- **Bootstrap-only surface (flags/env)**: the irreducible set needed before a DB exists: data directory, bind address, Docker socket path, initial admin bootstrap. Nothing else.
- **First run = UI setup wizard**: create the admin user; registry creds are optional and de-scoped from required setup. Secrets are never required in env/files where the DB can hold them encrypted.
- **Settings export/import (JSON)**: config stays version-controllable despite UI-first management; mitigates the lack of declarative/GitOps reproducibility.

---

## 10. Technology

- **Backend (Go):** router `chi`; Docker `github.com/docker/docker/client`; compose parse `github.com/compose-spec/compose-go`; compose writes by shelling to `docker compose` (fidelity); registry `github.com/google/go-containerregistry` (Hub/GHCR/OCI, anonymous-first auth, manifest lists, digests, labels); SQLite `modernc.org/sqlite` (pure-Go, no cgo → true static binary); embedded migrations; auth via session cookie + argon2id + CSRF.
- **Frontend:** React + Vite + TypeScript; TanStack Table + TanStack Query; built into the binary via `embed.FS`.
- **Job engine:** in-process worker pool; queue persisted in SQLite (survives restart, resumes interrupted jobs); no external broker.
- **Scheduler:** in-process tickers per policy.
- **Packaging:** single static binary + multi-arch container image (bundles the docker CLI + compose plugin); deployment options detailed in §11.

---

## 11. Deployment

dockbrr ships as a single static binary plus a multi-arch container image, and is configured UI-first (§9), so deployment carries only the bootstrap surface: data directory, bind address, Docker socket path. Pick the method that matches how you run Docker.

**Distribution.** Pre-built multi-arch images on GHCR (`ghcr.io/dockbrr/dockbrr:latest` + semver tags); per-OS/arch binaries on GitHub Releases. The image bundles the `docker` CLI + compose plugin so compose writes are byte-identical to a hand-run command. Default listen port **3625** (uncommon, avoids clashes with common self-hosted apps; mnemonic: `DOCK` on a phone keypad), configurable via `--listen`.

### 11.1 Docker Compose (recommended)

The recommended path. The image runs against the host's Docker socket and executes real `docker compose`.

```yaml
services:
  dockbrr:
    image: ghcr.io/dockbrr/dockbrr:latest
    restart: unless-stopped
    ports: ["3625:3625"]            # bind 127.0.0.1 behind a proxy (see §11.3)
    environment:
      - TZ=Europe/Paris
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock   # Docker control
      - ./data:/data                                # SQLite + state
      - /srv/stacks:/srv/stacks                     # compose project roots
```

**Path-identity rule (the one sharp edge).** Because dockbrr runs real `docker compose` against your files (Option A, §2), every compose project must be reachable inside the container **at the same absolute path the compose labels report** (`com.docker.compose.project.config_files`). Mount the parent stacks directory 1:1 (`/srv/stacks:/srv/stacks`), never remapped (`…:/app/x`). Stacks under unmatched paths are marked `unmanaged` (§7). If your stacks are scattered across the filesystem, prefer the native install (§11.2).

### 11.2 Native binary + systemd (secondary)

A standard Linux service install. Runs directly on the Docker host, so the socket, the `docker` CLI, and every project directory are already at their real paths: **no path remapping**. Requires the docker CLI + compose plugin on the host.

```ini
# /etc/systemd/system/dockbrr@.service   (template unit)
[Unit]
Description=dockbrr
After=docker.service
Wants=docker.service

[Service]
User=%i
Group=%i
ExecStart=/usr/local/bin/dockbrr --config=/home/%i/.config/dockbrr --data=/home/%i/.local/share/dockbrr
Restart=on-failure

[Install]
WantedBy=multi-user.target
# enable: systemctl enable --now dockbrr@USERNAME
```

Binary to `/usr/local/bin`; config in `~/.config/dockbrr`, data (SQLite) in `~/.local/share/dockbrr`. The service user must be in the `docker` group (or otherwise own the socket).

### 11.3 Reverse proxy (TLS)

dockbrr binds `127.0.0.1` by default and expects a proxy for TLS. Caddy:

```
dockbrr.example.com {
    reverse_proxy 127.0.0.1:3625
}
```

nginx: standard `proxy_pass http://127.0.0.1:3625` with `proxy_buffering off` for the SSE stream (§2). TLS terminates at the proxy; the session cookie is marked `Secure` when served over HTTPS.

### 11.4 Socket-proxy hardening (optional)

Front the Docker socket with a docker-socket-proxy (e.g. tecnativa) to shrink the API surface. **Honest scope:** this fully protects the read-only detection path (containers/images `GET`), but the Job Engine (§7) needs container create/start/recreate + image pull to apply updates, so a dockbrr that can apply updates cannot be locked to read-only. Grant the write endpoints the job engine uses; deny the rest (`exec`, secrets, swarm, system-wide). Net effect: a smaller blast radius, not zero.

### 11.5 Non-goals (with rationale)

- **Windows / macOS (Docker Desktop)**: the engine runs in a VM; the socket and project-directory paths differ from the host filesystem, breaking Option-A compose-write fidelity (§2). Not supported.
- **Shared-seedbox installers** (swizzin / saltbox / quickbox): target seedbox applications, not a Docker-host manager. Out of scope.
- **unRAID / Portainer one-click templates**: both already provide container update-management (update-ready indicators, recreate), so dockbrr largely overlaps there. Not prioritized; revisit on demand.

---

## 12. Extensibility (future types)

Abstractions kept clean so new providers slot into the generic Service/Update model:
- `Host`: local / tcp / ssh / agent.
- `WorkloadProvider`: compose / standalone / (future) k8s, podman, swarm.
- `RegistrySource`: pluggable registries.
- `ChangelogSource`: pluggable, ordered fallback chain.
- `Executor`: compose CLI / Docker API / agent RPC.

Discovery, detection, and UI all operate on the generic Service/Update model. **Multi-host** = add Host rows + an agent Executor; the domain is untouched.

---

## 13. Assumptions & open questions

- **Assumption:** the host running dockbrr has the Docker CLI + compose plugin available (true wherever Docker runs). If absent, compose-write actions degrade. A future API-based recreate path is the documented fallback, not v1.
- **Assumption:** compose project directories are reachable by dockbrr at the same absolute paths the compose labels report, automatic for the native install (§11.2); required of the compose deploy via path-identity mounts (§11.1). Stacks under unmatched paths are marked `unmanaged` (§7).
- **Open (deferred):** automated rollback policy; notification channels; multi-host transport/agent protocol; per-image update-channel rules (e.g., "track minor, not major").
