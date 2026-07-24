# Multi-job apply panel

## Problem

"Apply all" enqueues one service-scope apply job per pending update, but the
live panel (`ApplyPanel`) tracks only a single job id. In `ApplyAllButton` an
`opened` flag fires `onApplied(res.job_id)` exactly once, for whichever enqueue
HTTP response resolves first.

Consequences reported by the user:

- The tracked job can be queued behind its siblings under the per-project mutex.
  The SSE handler (`internal/httpapi/sse.go`) replays empty history and the live
  channel emits nothing until the job runs, so the panel sits on
  "Waiting for log output..." and looks frozen.
- The other N-1 jobs run and finish invisibly. They appear in the jobs list
  (where the user found them) but the panel only ever shows one container's log.
- Global "Apply all" spans multiple projects, whose jobs run concurrently (the
  mutex only serializes within a project), so there is no single "running" job
  to follow.

Row spinners are unaffected: `markServiceBusy` already fires per enqueue, so
every service animates; the user likely saw only one because the others sat in
collapsed project groups (`defaultCollapsed`).

## Goal

When apply-all enqueues 2+ jobs, show a panel that lists every job with live
per-job status, lets the user expand any job to watch its log, offers per-row
rollback on failure, and auto-closes only when the whole batch succeeds. Single
-job applies (row apply, ReviewDrawer) keep today's exact behavior.

## Architecture

Extract today's single-job panel body into a reusable `JobLogView`, then build a
bulk panel on top of it. The single-job path does not change visually.

- **`JobLogView`** (new, extracted from `ApplyPanel`): owns `useJob` +
  `useJobLog` for one job, renders the status line and the log box, and keeps the
  in-place rollback swap (internal `setJobId`). Props gate auto-close and the
  rollback offer so both hosts reuse it.
  - Props: `{ jobId, readOnly?, autoClose?, showRollback?, onClose? }`.
  - When `autoClose` is set and the job reaches `success`, it runs the 4s
    countdown and calls `onClose`. `readOnly` still suppresses rollback and
    invalidation, as today.
- **`ApplyPanel`** (refactored, same public shape): section chrome + title, then
  `<JobLogView autoClose showRollback onClose=... />`. Single-row apply,
  ReviewDrawer, the jobs screen, and history keep importing/using it unchanged.
- **`BulkApplyPanel`** (new): section chrome + aggregate header + a list of
  `JobRow`. Rendered only when a batch has 2+ jobs.
- **`JobRow`** (new, internal to `BulkApplyPanel`): service label + status badge
  + spinner + expand toggle. Expanded, it mounts `<JobLogView showRollback
  autoClose={false} />` lazily so the log SSE only subscribes when the row is
  open.

## Data flow

### Enqueue and reporting

`ApplyAllButton` stops collapsing N to the first response. It awaits all enqueue
POSTs and reports the full set once:

- Use `useApply().mutateAsync` over `pending`, gathered with `Promise.allSettled`
  (enqueue POSTs are sub-second; waiting for all is fine).
- On each fulfilled enqueue, `markServiceBusy(serviceId, jobId, "apply")` as
  today.
- After settling, call `onApplied(collected)` where
  `collected: { jobId: number; serviceId: number }[]` holds the successful
  enqueues. If none succeeded, do not open a panel.

`ApplyAllButton`'s `onApplied` prop type changes to
`(jobs: { jobId: number; serviceId: number }[]) => void`. This is that
component's own prop, distinct from the row / ReviewDrawer `onApplied(jobId:
number)`, which is left untouched.

### Panel selection in routes

`dashboard.tsx` and `project.$id.tsx` hold a discriminated panel state:

```
type PanelState =
  | { kind: "single"; jobId: number }
  | { kind: "bulk"; jobs: { jobId: number; serviceId: number }[] }
  | null;
```

- Single callers (row apply, ReviewDrawer) set `{ kind: "single", jobId }`.
- `ApplyAllButton`'s batch callback sets `{ kind: "bulk", jobs }`, **except** a
  batch of exactly 1 collapses to `{ kind: "single", jobId }` so a lone update
  renders the plain `ApplyPanel` with no bulk chrome.
- Render: `single` -> `<ApplyPanel jobId key={jobId} />`; `bulk` ->
  `<BulkApplyPanel jobs key=... />`.

Per-project apply-all lives inside `DashboardTable` (`ProjectBulkActions` ->
`ApplyAllButton`), so the batch callback is threaded table -> `ProjectBulkActions`
-> `ApplyAllButton` as a new prop, alongside the existing single `onApplied`.

### Aggregate status and auto-close

`BulkApplyPanel` polls the **original** apply job ids to compute the aggregate
and drive auto-close:

- Extract a `jobQueryOptions(id)` factory from `useJob` (single source of truth
  for the query key, fetcher, and terminal-status `refetchInterval`). Refactor
  `useJob` to consume it.
- `BulkApplyPanel` calls `useQueries({ queries: jobs.map(j => jobQueryOptions(j.jobId)) })`.
- Header: `Applying N updates - x/N done, y failed` where done = terminal
  statuses (`success|failed|canceled`) and failed = `failed|canceled`.
- Auto-close: when every original apply reaches `success`, run the 4s countdown
  (reuse the pattern from `ApplyPanel`) then `onClose`. If any is
  `failed|canceled`, never auto-close.

Keying the aggregate off the original apply ids (not the row's possibly-rolled
-back id) means a failed-then-rolled-back job still holds the panel open, honoring
"stay open if any failed".

### Service labels

`JobRow` resolves `serviceId -> name` from the already-cached projects/dashboard
query (the same data the route rendered from), rather than prop-drilling a name
map. This is required because `Job` from `/api/jobs/{id}` carries no name field;
only `JobRow` from the list endpoint does. Fallback label when the name is not
resolvable: `service #<id>`.

## Expansion behavior

- On open, auto-expand the first `running` job; if none is running yet, expand
  the first job. This fixes the original "sat on a queued job" symptom by
  surfacing a live log immediately.
- Rows toggle independently; multiple can be open at once (concurrent jobs across
  projects).
- A finished row can still be expanded; its log replays from the SSE replay
  prefix (`handleJobLogs` history replay).

## Failure / rollback

- A failed row shows its red status + error and the existing `RollbackButton`.
- Rollback swaps that row's displayed job in place via `JobLogView`'s internal
  `setJobId`, so the row follows the rollback's log and status.
- The batch's `y failed` count keys off the original apply outcome (see
  aggregate above), so a row mid-rollback does not flip the panel to
  "all succeeded".

## Testing

- **`ApplyAllButton`** (extends existing `BulkActions.test.tsx`): enqueues one
  service-scope apply per pending update; reports the full `{jobId, serviceId}[]`
  set (not just the first); marks each service busy; a 1-update batch still
  reports a single-element array.
- **`BulkApplyPanel`** (new): header counts reflect per-job statuses; auto-closes
  only when all succeed; stays open when one fails; expanding a row subscribes
  its log; a failed row shows the rollback button and swaps to the rollback job
  on click.
- **`JobLogView` / `ApplyPanel`**: existing single-job tests stay green as a
  regression guard on the extraction (title, status line, auto-close, rollback).
- **Routes**: a batch of 2+ renders `BulkApplyPanel`; a batch of 1 renders the
  single `ApplyPanel`.

## Out of scope

- No backend changes. The job engine, SSE log handler, and job APIs are reused
  as-is.
- No change to how jobs are scheduled/serialized (per-project mutex stays).
- No cross-batch persistence: closing the panel does not stop the jobs (they
  remain visible in the jobs list), matching today.
