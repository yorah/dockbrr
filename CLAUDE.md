# dockbrr

Self-hosted Docker compose update manager. Single static Go binary + embedded React SPA.

## Commands

Shortcuts (mise tasks: see `mise.toml`, list with `mise tasks`):

```bash
mise run build   # SPA + static binary -> ./dockbrr (restores dist/index.html placeholder)
mise run run     # build then launch
mise run check   # go vet + go test + web vitest
mise run dev     # API (:3625) + vite (:5173) together; open http://localhost:5173

```

Underlying commands (what the tasks run):

```bash
CGO_ENABLED=0 go build ./...   # must stay CGO-free (static binary invariant)
go vet ./... && go test ./...
cd web && npm test             # vitest
cd web && npm run typecheck && npm run build   # dist/ embedded by internal/httpapi/spa.go
```

Go 1.26 via mise. `rtk` proxy wraps common CLI commands (hook rewrites automatically).

⚠️ TS typecheck: use `npm run typecheck` (as above), NOT `npx tsc`, the rtk hook proxies
`npx tsc` and reports a false "No errors found" even when real type errors exist.
`npm run typecheck` invokes the TS 7 compiler (typescript-native) by explicit path;
`node_modules/.bin/tsc` is ambiguous since two TS versions are aliased in devDependencies
(classic TS 6 for typescript-eslint, real TS 7 for everything else). `npm run build` also
fails on type errors, so it's a reliable backstop.

## Architecture (internal/)

- `store`: owns SQLite: schema, migrations, all persistence. No business logic.
- `job`: Job Engine: persisted, per-project-serialized queue. **ONLY component allowed to mutate Docker.** Keyed mutex = one in-flight job per project.
- `compose`: whitelisted compose verb runner, exec'd as argv (never shell). Parse + PullSpec/UpSpec/ScopeTargets = single source of truth for command building (preview endpoint reuses them).
- `scan`: read-only detection orchestrator. Never touches Docker mutation.
- `detect`: compares running images vs remote registry state (digest + semver).
- `discovery`: groups containers into projects/services, reconciles into store.
- `registry`: resolves image refs to digests + OCI metadata.
- `changelog`: enriches updates with changelog text/URL (GitHub releases, registry description, OCI labels).
- `docker`: thin Docker SDK wrapper.
- `httpapi`: REST + SSE API + embedded SPA (`spa.go`). Deps container for injection.
- `auth`: argon2id password hashing, single-user.
- `secret`: data-encryption key, AES-256-GCM sealing.
- `config`: bootstrap config only (everything else lives in store settings).

`web/`: Vite + TS + Tailwind v4 + Radix/shadcn + TanStack (router/query/table/form). Stack decisions: docs/dev/specs/stack.md.

## Safety invariants (check every change + review against these)

1. Single static binary: CGO_ENABLED=0, SPA via embed.FS (only dist/index.html placeholder tracked).
2. UI/API never touches Docker directly, all mutation goes through the Job Engine.
3. Snapshot precedes every mutation; rollback targets latest snapshot.
4. Pull-before-up, always. Apply health-gate polls the RECREATED container ids (not pre-apply ids).
5. Per-project keyed mutex: one in-flight job per project.
6. Compose verbs whitelisted + exec'd as argv. No shell, no user-built command strings.
7. Frontend: CSRF header on mutations only, credentials include, 401→auth gate; GitHub token write-only (never echoed); changelog rendered via react-markdown + rehype-sanitize, no dangerouslySetInnerHTML; no CDN (self-contained, system fonts).

## Workflow (SDD)

Development runs as subagent-driven development. Process rules, dispatch contracts, and learned gotchas: **docs/dev/sdd-workflow.md**: read it before executing any plan.

If you have the `superpowers` Claude Code plugin available, use it for this repo's workflow: `superpowers:brainstorming` before designing a change, `superpowers:writing-plans` to turn it into a plan, and `superpowers:subagent-driven-development` (or `superpowers:executing-plans`) to implement one, per `docs/dev/sdd-workflow.md`. If you don't have it, the plan/spec docs below are still fully readable and usable on their own.

- Plans: `docs/dev/plans/` (completed ones in `archive/`).
- Design spec: `docs/dev/specs/2026-06-30-dockbrr-design.md`.
- Per-phase scratch + ledger: `.superpowers/sdd/phase-N/` (gitignored).
