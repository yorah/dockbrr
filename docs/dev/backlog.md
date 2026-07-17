# Deferred backlog

Durable home for Minors/follow-ups a review deferred rather than fixing in-branch.
The SDD ledger lives in gitignored worktree scratch and dies when the worktree is
removed, so before finishing any branch, flush its deferred items here (tracked
in git). One line per item; strike through or delete when done.

Format: `- [ ] [source-tag] short description, why deferred / what it needs`

## Open

### Workload lifecycle Phase 1 (start/stop/restart, remove, logs; branch `feat/loose-container-grouping`, 2026-07-15, whole-phase review GO)

Genuine defers from the whole-phase review. The other 5 review minors were fixed in-branch
(commit c4f16a5): restart double-parse, "gone"-state test, LogsDrawer stale-response guard,
frontend/backend stopped-state alignment, lifecycle event-kind labels.

- [x] ~~[wl-p1] Lifecycle jobs emit no SSE progress lines~~ FIXED (217f11): `Lifecycle` gained a nil-safe `Emitter`, emits progress lines through `Handle`.
- [x] ~~[wl-p1] Remove guard reads stale `store.Service.State`~~ FIXED (4bf86e4): `runRemove` now live-inspects the container (`InspectStatus` on the `Mutator`) and refuses if actually running/restarting, on top of the stored-state + kind guards.
- [x] ~~[wl-p1] No test covers the 4 new lifecycle event `KIND_META` entries~~ FIXED (fdf4fc0): parametrized test over started/stopped/restarted/removed labels in `EventItem.test.tsx`.

### Workload lifecycle Phase 2 (standalone SDK-recreate apply; branch `feat/loose-container-grouping`, 2026-07-16, fresh whole-branch review GO)

Non-blocking minors from the independent whole-branch review. The two Important findings
(precheck-before-mutation, health-gate fail-fast) and a dead-code minor (`ContainerRestart`)
were fixed in-branch (commit aa60cab); the anonymous-volume + cross-tag Important gaps were
fixed earlier (0cd5193).

- [x] ~~[wl-p2] `StandaloneApplier` emits no job log lines~~ FIXED (217f11): standalone apply/rollback now emit progress lines via a nil-safe `Emitter`.
- [x] ~~[wl-p2] `failApply` on a pre-mutation error marks the update failed~~ FIXED (9fe500f): inspect-precheck + snapshot-insert failures now use plain `fail`, leaving the update `available` for retry.
- [x] ~~[wl-p2] Cross-tag apply leaves `svc.ImageRef` on the old tag until reconcile~~ FIXED (9fe500f): `runApply` persists the new ref via `store.Services.UpdateImageRef` on a cross-tag apply.
- [x] ~~[wl-p2] Crash-window leftovers strand `<name>-dockbrr-old`~~ FIXED (a208b99): `recreate` is now idempotent (`ContainerIDByName` + `clearNameConflict` clears leftovers before recreating; name regex escaped in 4ac44b0).

### UX / lifecycle

- [x] ~~[smoke-2026-07-10] Prune Gone services / empty projects~~, SHIPPED (dashboard-lifecycle UX batch, 2026-07-11): `auto_remove_gone` setting (default on) + `gone_grace_seconds` (default 3600) + `services.gone_since`; discovery hard-deletes gone-past-grace services + empty discovered projects (FK cascade). Manual projects never pruned.

### Deferred minors: dashboard-lifecycle UX batch (2026-07-11, whole-branch review non-blocking)

- [x] ~~[dashux T4a]~~: ALREADY RESOLVED (backlog-flush 2026-07-12, verified in source): `discovery.go` clamps `graceSecs < 0` to 0 before the prune cutoff (a negative grace would push the cutoff into the future and sweep every gone service). Covered by `TestReconcileNegativeGraceClampedNotPushedIntoFuture`.
- [x] ~~[dashux T4b]~~: ALREADY RESOLVED (verified in source): the service-delete loop in `Reconcile` is `p.Source != "discovered"` gated (defense-in-depth, still INERT for manual projects). Covered by `TestReconcileGoneServiceInManualProjectNeverAutoDeleted`.
- [x] ~~[dashux T1]~~: RESOLVED. `TestSettingDefaultsMatchConsumers` now pins `auto_remove_gone`/`gone_grace_seconds` alongside the main.go defaults. (True literal-vs-map cross-check would need discovery.go to import httpapi.settingDefaults, an import-cycle risk, not worth it; the settingDefaults side is pinned.)
- [x] ~~[dashux T10]~~: MOOT (verified in source): the changelog disclosure is now nested inside `EventItem`'s single `<li>` (EventItem.tsx), not a sibling row. Refactor already happened.

- [x] [smoke-2026-07-10] ~~Pinned-after-apply UX gotcha~~: largely RESOLVED (autoupdate-minors, 2026-07-11). Compose file write-back keeps exact/floating-tag services un-pinned after apply (the file still tracks the tag, so `collect.go` never re-derives them as Pinned), and for the remaining `${VAR}`/non-rewritable fallback case, `EffectiveAutoUpdate` (projects.go) now vetoes only a GENUINE pin (`Pinned && !Drifted`); a fallback runtime-pin (`Pinned && Drifted`, file still tracks a tag) no longer opts the service out of auto-update. Only remaining nicety: a UI badge/tooltip for a service that IS genuinely pinned (user pinned a digest in their file), explaining why auto-update is off for it. Not blocking.

### Phase 7 web UI (feature merged, ledger head c24242e, deferred minors, whole-branch review non-blocking)

Flushed 2026-07-12 from the gitignored phase-7-web-ui SDD ledger before removing its
worktree. Whole-branch review deemed ALL of these deferrable (no merge-blockers).
Already resolved and NOT relisted: T13-M1 (rollback "Applied" mislabel) + the cross-seam
job-status vocabulary mismatch: both FIXED in `c24242e`; T14-M2 (rollback service-events
invalidation): MOOT (history ApplyPanel is read-only). Some items below may have been
incidentally addressed by later batches (batch3/4, dashux, autoupdate, apply-buttons), verify current source before acting.

Backend: all CLEARED in the backlog-flush batch (2026-07-12):
- [x] ~~[p7 T1-M1]~~: success test now asserts the cookie value + `MaxAge>0` and that it resolves to a live session (not name-only).
- [x] ~~[p7 T1-M2]~~: added `TestChangePasswordRevokesOtherSessions` (a 2nd-device session is invalidated by a password change).
- [x] ~~[p7 T1-M3]~~: added `errInternal` + `writeInternalError` (logs the real cause, returns a generic message); all auth/password/events 500s routed through it.
- [x] ~~[p7 T2-M1]~~: rollback pull/up now use the shared `compose.PullSpec`/`UpSpec` builders (byte-identical to apply, incl. service-scope `--no-deps`).
- [x] ~~[p7 T2-M2]~~: added `TestPullSpecProjectScope`.
- [x] ~~[p7 T3-M1]~~: added `TestServiceEventsMalformedID` (400 on non-numeric id).
- [x] ~~[p7 T6-M1]~~: `serveFile` sets Content-Length when the size is known.
- [x] ~~[p7 T6-M2]~~: SPA API-guard now runs on the CLEANED path (`//api/x` 404s); `TestSPAGuardsCleanedAPIPath`.
- [x] ~~[p7 T6-M3]~~: wired server test drives `/api/bogus` e2e (404, not SPA index).
- [x] ~~[p7 T6-M4]~~: `serveFile` falls back to `application/octet-stream` for extensionless files.

Frontend: CLEARED in the backlog-flush batch (2026-07-12):
- [x] ~~[p7 T9-M1]~~: login guards submit + disables Sign in until both fields filled.
- [x] ~~[p7 T9-M2]~~: AuthGate surfaces a `/setup/status` failure as an error banner (test added).
- [x] ~~[p7 T10-M3]~~: removed the unused `@radix-ui/react-dropdown-menu` dep.
- [x] ~~[p7 T12-M2]~~: `javascript:`-URL XSS assertion is now unconditional (no surviving anchor carries the scheme).
- [x] ~~[p7 T12-M3]~~: ReviewDrawer toasts on failed Apply/Dismiss/Restore.
- [x] ~~[p7 T13-M2]~~: useEventStream reconnects with capped exponential backoff after a transient error (tests added).
- [x] ~~[p7 T14-M3]~~: added EventItem tests (kind→icon map / unknown-kind / `ref_job_id` wiring) + a ServiceDetail past-digests test.
- [x] ~~[p7 T14-M4]~~: Past digests list excludes the service's current digest (shown in the header), no more double-listing; test added.
- [x] ~~[p7 T15-M1]~~: PasswordSettings distinguishes 401 ("Current password is incorrect") from any other failure ("Couldn't change password…"); tests added.
- [x] ~~[p7 freshness]~~: ApplyPanel invalidates projects/updates/jobs on a live job's terminal status, closing the SSE-dropped freshness gap.
- [x] ~~[p7 T10-M2]~~: MOOT (verified in source): `tw-animate-css@1.4.0` is a dep and imported in `index.css`, so `animate-in`/slide classes are backed. (Note referenced the old `tailwindcss-animate`; the v4 replacement is present.)

Accepted-by-design (recorded, not bugs): T5-M1 (`.gitignore` adds `*.tsbuildinfo`, sensible); T8-M1/M2 (report-accuracy nits, no code defect); T15-M2 (tri-state via Select, no tri-state Switch primitive, reuse correct); presenters use `text-*-500` without `dark:` (legible in both, brief-verbatim); T10-M1 (Dialog/Drawer duplicate Overlay/Title/Description, shadcn copy-own convention); T11-M2 (update-available filter doesn't exclude pinned) + T11-M3 (search ORs in project name), both brief-inherited spec semantics, not defects; T12-M1 (changelog render path latent until the API carries `changelog_text`, frontend is wired correctly, backend fill is the gate); T14-M1 (service-detail StatusBadge can't show update-available, the dashboard is the update surface in v1).

### GitHub changelog repo resolution (feature merged 2026-07-12, deferred minors, whole-branch review non-blocking)

All six CLEARED in `cd8ba3a` (2026-07-12).

- [x] ~~[ghrepo cache-1] positive-cache write-amplification~~, `Resolve` now `Put`s only on a cache miss (a `cached` flag guards it); positive hits no longer re-stamp `resolved_at`. Comment added.
- [x] ~~[ghrepo cache-2] cache key omitted labels~~, key is now the resolved `owner + "/" + name`, not `repoFromRef(ref)`, so the label-derived mapping is what's remembered.
- [x] ~~[ghrepo cache-3] `cache.Get` errors swallowed~~, now logged (`changelog: cache get ...`), symmetric with `Put`.
- [x] ~~[ghrepo T3-M2] no auth assert on raw CHANGELOG.md request~~, `ghServer` asserts `wantAuth` on the raw handler too; `TestGitHubChangelogLinkTokenSent` covers it.
- [x] ~~[ghrepo T1-M3] python/golang curated entries untested by name~~, added `curated remap python` (→python/cpython) + `curated remap golang` (→golang/go) rows.
- [x] ~~[ghrepo T3-M1] dead nil-check in `changelogLink`~~, collapsed to `if tok := s.tokenFn(); tok != ""`.
- (also added `TestGitHubPositiveCacheHitDoesNotReput` asserting a positive hit still fetches notes but does not re-Put.)
- Accepted-by-design (spec, NOT bugs): wrong-repo coincidence bounded by exact tag-match + user-dismissible; >100-releases target misses (one page, falls through to Docker Hub); postgres beta/rc tags (`REL_17_BETA1`) not covered by `postgresTags`.

### Compose write-back (feature merged: deferred minors from whole-branch review)

- [x] ~~[writeback T3]~~: ALREADY COVERED (verified in source): `TestReconcileDriftOddNameDriftedWhenImageDiffersFromDeclared` + `TestReconcileDriftOddNameNotDriftedWhenImageMatchesDeclared` pin the underscore-named drift case in `discovery_test.go`.
- [x] [writeback T4] ~~`handleGetSettings` special-cases `write_back_compose` with an `else if` branch~~ (MOOT (autoupdate-minors, 2026-07-11): verified `internal/httpapi/settings.go`) the special-case branch is gone; `write_back_compose` now flows through the generic `settingDefaults` map lookup in `handleGetSettings` alongside every other key (collapsed in the dashboard-lifecycle batch).
- [x] ~~[writeback T6]~~ (ALREADY DONE (verified in source): `rollbackPullUp(ctx, job, svc, proj, prevDigest string, files []string)` already takes `prevDigest`, not the full Snapshot) the signature was narrowed by a later batch.

### Batch 4 (all 9 tasks done, see docs/dev/plans/2026-07-04-batch4-feature-completion.md)

Remaining open: needs a LIVE Docker host (can't run in build sandbox):

- [ ] [batch4 Task 10] Manual smoke against a LIVE Docker host: semver version-delta + severity color; air-gap toggle stops changelog fetches without restart; stop a container → badge flips within seconds (docker events + SSE); Jobs screen history; settings export→wipe→import round-trip. Automated suite is green.
- [ ] [batch4 T8-live] `docker.ContainerEvents` live-daemon path (message decode, filter correctness against a real Events stream) is compiler-verified only, covered by the Task 10 manual smoke above.

Won't-fix:

- [x] [batch4 T1-M2] ~~`semver.go` `ContainsAny("-+")` redundant with parseCore~~, FALSE minor: `parseCore` STRIPS pre-release/build metadata (semver.go:79-85), so `ContainsAny("-+")` is the ONLY pre-release exclusion. Load-bearing; removal would break the `2.0.0-beta1`→excluded test. Reviewer was wrong. Kept.

## Done

- [x] [batch4 T3-M3] `gone` + `restarting` added to Filters `STATUS_OPTIONS`; `joinRows` filters on them, and `status=gone` shows gone rows even with Show-removed off. Tests in `useDashboardRows.test.tsx`.
- [x] [batch4 T4-M1] `scan` now dedups the notify hint per service by to-digest (`notifiedTo` map); a standing drift fires once, a cleared+re-drift re-fires. `TestCheckServiceNotifyDedups*`.
- [x] [batch4 T4-M2] `/api/events/stream` emits an SSE `: heartbeat` comment every `heartbeatInterval` (25s) to survive idle-socket reapers. `TestEventStreamSendsHeartbeat`.
- [x] [batch4 T4-M3] `Reconcile` now returns `changed bool` (fingerprints the discovered surface); `reconcileLoop` publishes `reconciled` only on a real change. `TestReconcileReportsChangedOnlyOnRealChange`.
- [x] [batch3 env] rtk-masks-`npx tsc` designed out: `CLAUDE.md` now documents `./node_modules/.bin/tsc` (rtk-bypassing) + `npm run build` as the TS verification path. Not fixable in-repo beyond the doc; env limitation.
- [x] [batch4 env] favicon gitignore rule (`/internal/httpapi/dist/favicon.svg`) verified holding via `git check-ignore` (0178b43 batch, rule added `f1f2a52`-era).
- [x] [batch4 T1-M1] doc comment added at Detect's cache-hit early-return: a cache hit intentionally skips the semver tag scan until TTL expires (`6a5cfe9`).
- [x] [batch4 T2-M1] `NewApplier` now nil-guards `healthTimeout`/`healthPoll` (defaults 2m/2s), symmetric with detector/resolver, panic-safe (`6a5cfe9`).
- [x] [batch4 T2-M2] `TestDetectCacheTTLReadPerCall` proves cacheTTL closure read per Detect (`6a5cfe9`).
- [x] [batch4 T3-M1] `DashboardStats.test.tsx` asserts `gone` NOT counted in Stopped tile (`6a5cfe9`).
- [x] [batch4 T3-M2] `Filters.test.tsx` created, covers the "Show removed" switch toggle → onChange showRemoved (`6a5cfe9`).
- [x] [batch4 T6-M1] `handleImportSettings` rejects `version != 1` with 400 before applying; `TestSettingsImportRejectsUnknownVersion` (`6a5cfe9`).

- [x] [batch3 final-M2] `web/src/components/ui/table.tsx` viewport-cap fragility fixed: AppLayout/main/dashboard are now a flex column and the dashboard table fills remaining space via `wrapperClassName="min-h-0 flex-1"` (generic default kept `max-h-[calc(100dvh-14rem)]` for standalone tables like settings). No more fixed-constant overflow when stats/filters rows wrap.
- [x] [batch3 final-M1] `docker_reachable` now a live per-request probe: added `DockerPinger` iface to Deps, `status.go` Pings with a 2s timeout; `main.go` wires a dedicated probe client (survives boot even if daemon down, so recovery is detectable). nil pinger → false.
- [x] [batch3 final-M3] Added `stopped` status to `computeStatus`/`StatusBadge` + shared `STOPPED_STATES`/`isStopped` in StatusBadge; DashboardStats + useDashboardRows reuse it. Badge and tile now agree.
- [x] [batch3 final-M5] Tile clicks now reset to `DEFAULT_FILTERS` before applying the patch (dashboard.tsx): Services tile is a true "show everything" reset.
- [x] [batch3 final-M4] `status_test.go` adds explicit `TestStatusRequiresAuth` (401) + `TestStatusDockerUnreachable` (ping-fail and nil-pinger → false).

- [x] [batch3 T3-M1] per-row `<TooltipProvider>` → single provider at `AppLayout` root.
- [x] [batch3 T4-M1] removed dead `onlyUpdates?` member from `DashboardStatsProps.onFilter`.
- [x] [batch3 T6-M1] added `beforeEach(vi.clearAllMocks)` to `mutations.test.tsx`.
- [x] [batch3 T6-M2] loading skeleton now has `role="status"`.
- [x] [batch2 T2-M1] `RecordDrift` re-opens `superseded` on drift-back, fixed in `0178b43`.
- [x] [batch2 T1-M1] preview_test asserts `/preview` emits `-p app`, fixed in `0178b43`.

## Sidebar shell + design-token restyle (2026-07-12), deferred Minors

Whole-branch review verdict: READY TO MERGE. All 7 safety invariants hold. None of
the below block merge; all were triaged DEFER.

- [x] [sidebar T1-M1] ~~Dark `--primary` (#3b82f6) on `--primary-foreground` (#f8fafc) is ~3.5:1, under AA 4.5:1 for normal text.~~ FIXED (`205a7b5`): dark `--primary` → `#2563eb` (~4.9:1). `:root`'s `--primary` and `--ring` untouched.
- [x] [sidebar T1-M3] ~~`--radius` is declared in `:root` but not mapped in `@theme inline`, so it's inert.~~ FIXED (`76643e9`): mapped `--radius-sm/md/lg`; verified in built CSS that `rounded-sm/md/lg` derive via `calc(var(--radius) ...)`.
- [x] [sidebar T2-M1] ~~`useSidebar.ts`: unnecessary `as (e: MediaQueryListEvent) => void` casts.~~ FIXED (`24364b4`): dropped, same handler reference passed to add/removeEventListener.
- [x] [sidebar T3-M1] ~~`useProjectHealth.ts`: `Map<number, number>` sentinel-value-1 lookup.~~ FIXED (`24364b4`): replaced with `Set<number>`.
- [x] [sidebar T6-M1] ~~`project.$id.tsx`: `scoped` rebuilt every render; `filters.project` dead state.~~ FIXED (`586e6dd`): `scoped` wrapped in `useMemo([filters, id])`; `project` dropped from the `useState` initialiser (local state is `Omit<FilterState, "project">`).
- [x] [sidebar T8-M1] ~~`SeverityDelta`: `digest-only` both a map entry and the `??` fallback, so deleting it wouldn't fail the test.~~ FIXED (`de6cf9c`): map exported as `SEVERITY_COLOR`; new test pins the exact key set. Verified: deleting the `digest-only` entry fails the new test; restoring it passes.
- [x] [sidebar T4-M1] ~~`renderApp` (test utils) is async only to dodge a partial `vi.mock` of `@tanstack/react-router`; a bare un-awaited call compiles and races.~~ FIXED (`076ccf1`): `service.$id.test.tsx`'s mock now spreads the real module via `importOriginal`, so `renderApp` is synchronous again with a static `import { routeTree } from "@/router"`; callers no longer `await` it.

## Settings revamp (phase 8, branch `settings-revamp`, 2026-07-13)

Whole-branch opus review verdict: READY TO MERGE. All 7 safety invariants hold (3/4/5/6
verified untouched: no file under internal/{job,compose,scan,detect,store,registry,
discovery,secret,auth}/ is in the branch diff). Settings-clobbering across the four pages
that now edit the one settings object is impossible: the server PUT is a per-key patch, and
each page's `changed()` emits only its own edited keys. None of the below block merge.

All p8 code items below were FIXED in-branch before merge (commits `408fadf`, `70fa170`, `0eae560`).

- [x] ~~[p8 T1-M3] `/api/system/info` worst-case latency is 4s, not 2s (`dockerReachable` opens its own 2s timeout, then `ServerVersion` opens a second).~~ FIXED (`408fadf`): both probes now share ONE 2s budget via `dockerReachableCtx(ctx)`; `/api/status` keeps its own timeout, semantics unchanged. Test asserts both fakes observe the SAME deadline.
- [x] ~~[p8 T3-M2] `SettingsCard`'s header row has no wrap/`min-w-0` guard.~~ FIXED (`70fa170`): `min-w-0` on the text block + `break-words` on title/description; pinned by a test.
- [x] ~~[p8 T7-M1] No test covers the dashboard's Add-project triggers.~~ FIXED (`70fa170`): new `dashboard.test.tsx` (3 tests) pins the action-row button and the empty-state CTA. Their accessible names are deliberately distinct ("Add project" vs "Add your first project"): do not collapse them; three same-named buttons was a real a11y finding (`65d4bdd`).
- [x] ~~[p8 WB-M1] `useSettingsForm` re-seeds on any `data` change, discarding a user's unsaved edits.~~ FIXED (`70fa170`) (but the first fix REGRESSED something worse and was caught in review: skipping the whole re-seed once *any* field was edited left untouched fields on a stale baseline, so they were PUT with old values (edit poll_interval, refetch brings `concurrency:"8"`, save PUTs a stale `concurrency:"4"` and reverts the server). Made likelier by import now invalidating `keys.settings`. FINAL FIX (`0eae560`): re-seed PER KEY) keep only keys the user actually diverged on, follow the server on the rest, and always advance the baseline ref. The dirty-form test now asserts the PUT BODY (it previously mutated a second key and never asserted on it, exactly where the bug hid).
- [x] ~~[p8 WB-M2] `ScanningSettings`/`UpdatesSettings` return `null` while loading; `ApplicationSettings` renders a skeleton.~~ FIXED (`70fa170`, `0eae560`): all four settings pages (incl. Logs) now share the same skeleton pattern.
- [x] ~~[p8 WB-M3] Settings import persisted an invalid `log_level`, never applied a valid one, and left the Logs page showing a stale level.~~ FIXED (`408fadf` + `70fa170` + `0eae560`): import now reuses the PUT path's validation (rejected import 400s and persists NOTHING (validation fully precedes writes) and applies the level live; `useSaveSettings` invalidates `keys.logConfig` centrally whenever `log_level` is in the patch; the Logs `<select>` is controlled by query data (it was uncontrolled, so it kept showing the stale level even after invalidation) confirmed in the DOM before fixing) with an optimistic update on `onSettled` so a FAILED save can't strand it on a level that wasn't persisted.
- [ ] [p8 VISUAL] Nobody has looked at it in a real viewport, jsdom evaluates no Tailwind and no media queries, and the sandbox has no display. The `md`-breakpoint horizontal scroller and the active-row highlight are pinned by class/`aria-current` assertions but have never been *seen*. Worth a 30-second eyeball.
- [ ] [p8 WB-M4] Settings import is not transactional at the store level (no DB tx; keys are written one by one, then registries). A *rejected* import now persists nothing (validation precedes all writes), but a store/IO error mid-loop still 500s with earlier keys written, structurally identical to the pre-existing PUT behavior, and import is idempotent so a re-import heals it. Making it atomic needs a store-level tx API that `httpapi` does not have today.

Known-accepted (not defects, recorded so they aren't "rediscovered"):
- `(*docker.Client).ServerVersion` has no unit test at any level (the httpapi tests stub the func field): consistent with how `Ping` is treated in this codebase. It was, however, exercised against a real daemon during branch verification (reported v29.6.1 / API 1.55).
- `useJobs()` is capped at 100 rows, so a project whose last job aged out of that window shows no red dot in the sidebar. Conscious tradeoff vs. adding a per-project endpoint; documented in `useProjectHealth`.
- `ApplyPanel`'s log pane is theme-aware (`bg-muted`), not an always-dark terminal. Every other mono surface in the app (CommandPreview, ComposeModal) is `bg-muted`, and an always-dark pane would be the one theme-ignoring surface.

## Dashboard last-applied changelog (2026-07-13, branch `worktree-dashboard-last-changelog`)

Whole-branch review (opus): all 7 safety invariants HOLD. The one Important finding was
FIXED in-branch (`006a765`): the Changelog cell resolved `update ?? lastApplied` on presence
of a *pending update* rather than presence of a *changelog*, so a changelog-less drift
(non-GitHub image, no token, rate limit) made the last-applied changelog vanish again, the exact symptom the branch exists to remove. It now resolves to the first candidate that
actually has content. Deferred, none merge-blocking:

- [x] [lac-M1] FIXED: added a nullable `applied_at` column (migration 0007) + `Updates.MarkApplied`
      (sets `status='applied'` and `applied_at=CURRENT_TIMESTAMP` in one statement, used by both
      apply call sites in `internal/job/worker.go`). `ListLastAppliedByService` now orders by
      `COALESCE(applied_at, detected_at) DESC, id DESC` (subquery and doc comment updated to
      match), so legacy rows applied before the migration still order by `detected_at`.
- [ ] [lac-M2] After a rollback (`MarkRolledBack` flips `applied`→`rolled_back`), a service whose
      ONLY applied update was rolled back shows `: ` again in the Changelog column: `joinRows`
      maps only `available`/`dismissed` into `r.update`, so the `rolled_back` row (which IS in
      `ListVisible`) never surfaces. Not a regression (the column was empty there pre-branch) and
      arguably right: the running image is the pre-update one. Where an older applied row exists,
      the column correctly shows it (the rollback target).
- [x] [lac-M3] FIXED: dropped the dead `rolledBack: r.update.status === "rolled_back"` property from
      the `computeStatus` call site in `DashboardTable.tsx` (it could never be true (see lac-M2)       so it was pure noise); left a comment there so it isn't "restored" later. `computeStatus` /
      `StatusBadge`'s own `rolledBack` API is unchanged, and `joinRows` still deliberately does not
      surface rolled_back updates (lac-M2, declined separately).
- [ ] [lac-M4] `GET /api/updates/last-applied` ships full `changelog_text` for every service that ever
      had an apply, on every dashboard mount and after every `job_finished`. Same precedent as
      `/api/updates` (which also ships full text). If an install grows large, switch to a text-less
      list + on-demand fetch.

Known-accepted:
- The dashboard column and the service-detail history timeline cannot disagree: the timeline
  LEFT JOINs `updates` on `(service_id, to_digest)` per event and is untouched; the dashboard
  shows one row per service. Both are views of the same retained row.

## Changelog rate-limit signal (2026-07-17, merged from feat/changelog-rate-limit-signal)

Non-blocking Minors deferred from the whole-branch review (behavior correct, gaps are test-only):
- [x] [crl-M1] FIXED: changelog/github_test now covers both a positive 429+remaining:0 case (TestGitHubRateLimitedYieldsErrRateLimited table over 403/429) and an explicit "403 header-absent" negative case (403-header-absent subtest).
- [x] [crl-M2] FIXED: scan_test TestCheckServiceClearsRateLimitedStatusOnSuccess drives the full round-trip through CheckService (seed rate_limited -> resolve returns content -> changelog_status back to '' + content persisted).

## Current-version changelog for up-to-date services (2026-07-17, feat/current-version-changelog)

Non-blocking Minors deferred from the whole-branch review (behavior correct, gaps are test-only):
- [x] [cvc-M1] FIXED: added TestListLastAppliedTieBreakIgnoresIDAndTimestamp (updates_test.go) inserting applied first, current second (higher id, equal-or-later ts) and asserting applied still wins, isolating the ORDER BY (status='current') key from the id/timestamp fallback.
- [x] [cvc-M2] FIXED: scan create-row test now sets ImageVersion="0.0.0-label" distinct from ResolvedVersion="1.2.3" and asserts the row version is "1.2.3", proving ResolvedVersion wins the precedence.

## Self-update notification (2026-07-17, branch `feat/self-update-notification`, all-tasks review clean)

Two review minors were addressed in-branch (commit 50d2f32): the silently-swallowed GitHub
failure now logs at debug in both the handler and the startup-warm path, and `readCache`
gained a `url==""` guard against a partial 3-key cache write. Remaining are test-only gaps
(behavior correct) plus one accepted design point.

- [ ] [su-M1] No test exercises the `tokenFn`/`Authorization: Bearer` header path in the checker
      (`internal/selfupdate/checker.go`); token wiring is currently unverified.
- [ ] [su-M2] `writeCache` sets 3 settings keys non-atomically; a `Set` error mid-write leaves a
      partial cache. Low risk (in-proc SQLite) and the `readCache` `url==""` guard now masks the
      common partial, but a genuine atomic write / single-row encoding would be cleaner.
- [ ] [su-M3] No test covers context cancellation propagating into the outbound GitHub HTTP request.
- [ ] [su-M4] The nil-dep handler test asserts only `update_available:false`, not that `checked_at`
      is absent from the response.

Known-accepted:
- [su-A1] `UpdateNotice` reads the per-version dismissal from localStorage once at mount, so a
  dismissal doesn't re-sync across browser tabs. Acceptable for a single-user local app.
