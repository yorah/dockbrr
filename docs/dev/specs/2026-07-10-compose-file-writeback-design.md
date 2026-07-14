# Compose File Write-Back: Design Spec

**Date:** 2026-07-10
**Status:** Approved (brainstorm complete; ready for planning)

## Goal

When dockbrr applies an update, make the change **durable in the user's compose
file**: bump the declared image so the file and the running container stay in
sync: while preserving the user's chosen tag granularity and never mangling
their file. Eliminates the current file↔runtime drift (and the footgun where a
user's own `docker compose up` reverts a dockbrr apply).

## Background: current behavior

Apply today (`internal/job/worker.go`) writes a temporary
`dockbrr-rollback-*.yml` override pinning `image: repo:tag@digest`, passes it as
an extra `-f` to `docker compose up`, then deletes it. **The user's compose file
is never touched.** Consequences:

- The running container ref carries `@sha256:` → `collect.go:57` flags the
  service **Pinned** → `EffectiveAutoUpdate` (`projects.go:159`) excludes it from
  auto-update. Every manual apply silently opts the service out of auto-apply.
- **Drift footgun:** the file still declares the old tag; a user-run
  `docker compose up` reads only their file and reverts the container.

## Core rule: preserve granularity

Apply branches on the granularity of the **user's** image tag (as declared in
the file), and NEVER increases pin specificity.

| User's tag | Class | Apply does | File | Digest-pinned? |
|---|---|---|---|---|
| `latest`, `1`, `1.31` | floating | `compose pull && up` on the user's file | untouched | **no** (floats) |
| `1.31.2` + literal in file | exact, rewritable | surgical edit `1.31.2`→`1.32.0`, then `pull && up` | **edited** | no |
| `1.31.2` but `${VAR}` / multi-file / anchor | exact, not rewritable | runtime digest-pin override (today's path) | untouched | yes → surfaced as Drift |
| `repo@sha256:…` | digest pin | repin to new digest (today's behavior) | untouched | yes |

Key shifts from today:

- **Floating tags stop getting digest-pinned.** A floating tag already expresses
  "follow this stream"; `pull && up` achieves the update and the container ref
  stays floating (`nginx:1.31`), so the spurious Pinned badge disappears for
  them. Compose recreates the container because the pulled image ID changed.
- **Exact tags get written back**, preserving exact→exact form (`1.31.2` →
  `1.32.0`), never appending a digest: keeps the file clean and re-pullable.

When the global setting `write_back_compose` is **off**, ALL classes use the
runtime-pin path (today's behavior) regardless of granularity.

### Granularity classifier

A pure function over the tag string:

- `latest`, or any non-semver moving alias → **floating**.
- Semver-shaped with missing components (`1`, `1.31`) → **floating** (partial
  pin; the user intentionally tracks the newest within that prefix).
- Fully-specified semver (`1.31.2`, optionally with pre-release/build) → **exact**.
- Ref containing `@sha256:` → **digest**.

The new version dockbrr writes must match the existing tag's shape. This spec
only *writes* for the exact class, so the classifier's write path is
exact→exact; floating/digest never write.

## Rewrite mechanism

The risky part: editing YAML without destroying it.

1. Parse each of the project's raw config files with `yaml.v3` into a
   `yaml.Node` tree (preserves line/column + formatting).
2. Locate the `services.<svc>.image` scalar node. The image is **rewritable**
   iff ALL hold:
   - the node exists as a plain scalar (not produced by an anchor merge),
   - its value is a literal (contains no `${…}` interpolation),
   - its value's repo matches the running container's ref repo.
3. If the literal appears in more than one config file, edit the
   **highest-precedence** file (the last `-f` in compose precedence order).
4. **Textual replace on that single line only**: swap the tag substring within
   the located line. Never re-marshal the whole document (that would reflow
   comments, quoting, key order, anchors).
5. **Atomic write**: write to a temp file in the same directory, then `rename`
   over the original.
6. Any failure in step 2 → the image is **not rewritable** → fallback path
   (runtime digest-pin override, today's mechanism). Surfaced via Drift.

GitOps: no special guard. Editing a git-tracked file is intended (the user
commits the bump). Atomic write guarantees no partial-write corruption.

## Rollback

Reuses the existing `state_snapshots.compose_blob` column (present in schema,
currently unpopulated).

- **On a file-editing apply:** before editing, store the pre-edit file as JSON
  `{"path": "<abs path>", "content": "<verbatim file bytes>"}` into
  `compose_blob` of the snapshot row. Only one file is ever edited per apply, so
  a single `{path, content}` suffices.
- **Rollback:** read the service's latest snapshot. If `compose_blob` is
  non-empty → restore that file's content atomically, then `pull && up` scoped
  to the service. If empty → today's digest-repin path (floating / fallback /
  digest applies never edited a file, so `compose_blob` stays empty for them).
- **No `.bak` file**: the pre-edit content lives in the DB snapshot, keeping the
  project directory (often git-tracked) clean.

## Drift detection & badge

A derived state surfaced on the dashboard, computed during discovery.

- Reuse the existing read-only `compose.Parse` to resolve each service's
  **effective declared image** (this resolves `${VAR}` via compose's env/.env
  handling).
- Compare it to the running container's image ref (`collect` `ImageRef`).
- **Drifted** when they diverge. Cases:
  - floating (`file: nginx:1.31`, `running: nginx:1.31`) → match, not drifted.
  - rewritten exact (`file: 1.32.0`, `running: 1.32.0`) → match.
  - runtime-only fallback (`file: 1.31.2`, `running: 1.31.2@sha256:…`) → **drifted**.
  - out-of-band manual edit / stale container → **drifted**.
- Cost: one `compose.Parse` per project during discovery. `compose.Parse`
  already exists and is read-only.

The Drift badge is how the "runtime-only, file unchanged" fallback is surfaced, it also catches all other file↔runtime divergence.

## Setting & data model

- **Setting:** `write_back_compose` (bool, default **true**: opt-out) in the
  settings store. GeneralSettings gets a toggle "Write updates back to compose
  files" wired through the existing batch-save form + dirty indicator.
- **No schema migration.** Reuse `state_snapshots.compose_blob`.
- Drift is derived at discovery time; no new persisted column.

## Testing

**Unit (pure):**
- granularity classifier: `latest`/`1`/`1.31`→floating, `1.31.2`→exact,
  `repo@sha256`→digest.
- rewritability detector: literal (rewritable) vs `${VAR}` vs image-from-other-
  file vs anchor-merged (all not rewritable); repo-mismatch → not rewritable.
- surgical edit: preserves comments, quoting, key order, surrounding services;
  only the target line's tag changes; new tag preserves exact→exact and never
  appends a digest.

**Store:**
- `compose_blob` round-trip: store `{path, content}` → restore verbatim bytes.

**Job Engine:**
- exact-pin apply edits the target file AND writes the pre-edit blob to the
  snapshot.
- floating-tag apply runs `pull && up` on the user's file with **no** override
  (assert no `dockbrr-rollback-*.yml` in the compose invocation, no digest-pin).
- not-rewritable exact apply takes the fallback (runtime digest-pin), leaving the
  file untouched.
- rollback with a populated `compose_blob` restores the file then `pull && up`.
- `write_back_compose=false` forces the runtime-pin path for all classes.

**Discovery:**
- drift computed correctly for the four cases above.

**Web:**
- Drifted badge renders in the status column.
- write-back setting toggle appears, participates in the dirty indicator, and
  saves.

## Out of scope

- Rewriting non-image fields.
- Multi-file simultaneous edits (only the highest-precedence literal is edited).
- An explicit "unpin"/"un-drift" one-click action (the update flow itself
  resolves drift; a dedicated affordance can be a later follow-up).
- The Pinned-after-apply UX note and Gone/empty-project pruning tracked
  separately in `docs/dev/backlog.md`.
