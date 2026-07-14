# Job removal & retention: design

Date: 2026-07-13
Status: approved

## Problem

The `jobs` table grows without bound. Every check, apply, rollback and sync leaves a row
(plus its `job_logs` lines) forever. There is no way to clear job history from the UI, and
no automatic pruning.

## Scope

1. A **"Clear finished"** button on the Jobs screen that deletes every terminal job.
2. A **`job_retention_days`** setting that prunes finished jobs older than N days, daily.

Out of scope: per-row delete, count-based caps, deleting queued/running jobs, purging
`state_snapshots` or `service_events`.

## Referential integrity

Deleting a job row is safe with the existing schema (`0001_init.sql`):

| Table | FK to jobs | On delete |
|---|---|---|
| `job_logs.job_id` | NOT NULL | `CASCADE`: logs die with the job |
| `state_snapshots.job_id` | nullable | `SET NULL`: snapshot survives, rollback still finds it by service |
| `service_events.ref_job_id` | nullable | `SET NULL`: event history survives, loses its job link |

Rollback targets the latest snapshot **for a service**, not for a job, so clearing job
history never breaks rollback. No migration is needed.

## Store

`internal/store/jobs.go`:

```go
// DeleteFinished removes every terminal job (success|failed|canceled) and, via
// ON DELETE CASCADE, its logs. Queued and running jobs are never touched.
func (j *Jobs) DeleteFinished() (int64, error)

// DeleteFinishedBefore removes terminal jobs created before t.
func (j *Jobs) DeleteFinishedBefore(t time.Time) (int64, error)
```

Both return rows affected. The predicate is `status IN ('success','failed','canceled')`;
the age predicate uses `created_at`, which is `NOT NULL` and monotonic, a job that somehow
lacks `finished_at` still prunes.

## API

`DELETE /api/jobs` → `handleClearJobs` in `internal/httpapi/jobs.go`. Registered inside the
authenticated group, so it carries session auth + the CSRF check that applies to mutations.
Returns `{"deleted": n}`. Publishes `Event{Type: "jobs_cleared"}` on the bus so other open
tabs refresh.

## Setting

`job_retention_days`, added to the defaults map and the non-secret spec list in
`internal/httpapi/settings.go`. Default `"30"`. `0` disables pruning.

Consequence, accepted: on the first boot after upgrade, jobs older than 30 days are deleted.

## Pruner

`pruneLoop(ctx, settings, jobs)` in `cmd/dockbrr/main.go`: prunes once at boot, then on a
24h ticker. Each run re-reads `job_retention_days`; `<= 0` skips. Deletes finished jobs
created before `now - N*24h` and logs the count when non-zero. Independent of
`poll_interval_seconds` and of the scheduler loop.

## UI

- `web/src/routes/jobs.tsx`: a "Clear finished" button in the header, disabled when the list
  holds no terminal job. A Radix AlertDialog confirms: "Delete N finished jobs and their
  logs. Queued and running jobs are kept." On confirm, `useClearJobs` mutation → invalidate
  `keys.jobs` → toast with the deleted count.
- `web/src/components/settings/GeneralSettings.tsx`: a number input for retention days with
  a HelpTooltip explaining that 0 disables pruning and that only finished jobs are removed.

## Testing

- Store: queued/running survive both deletes; logs cascade; a snapshot whose job is deleted
  survives with `job_id` NULL; age predicate excludes recent jobs.
- API: `DELETE /api/jobs` returns the count and leaves running jobs; unauthenticated request
  is rejected.
- Web: the Jobs screen renders the button, the confirm dialog gates the mutation, and the
  button is disabled with no finished jobs.
