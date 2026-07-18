import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ApplyPanel } from "@/components/ApplyPanel";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useJobs } from "@/hooks/queries";
import { useClearJobs, useRollback } from "@/hooks/mutations";
import type { JobRow } from "@/api/types";

const STATUS_VARIANT: Record<string, "default" | "success" | "warning" | "danger" | "info"> = {
  success: "success", failed: "danger", canceled: "default", running: "info", queued: "warning",
};

const FINISHED = new Set(["success", "failed", "canceled"]);

// Rollback restores the service's LATEST snapshot, so it is only offered on
// the most recent finished apply per service: on an older row it would restore
// a newer state than the row suggests.
function latestApplyIds(jobs: JobRow[]): Set<number> {
  const ids = new Set<number>();
  const seen = new Set<number>();
  for (const j of jobs) {
    // newest-first list order
    if (j.type !== "apply" || j.service_id === null || !FINISHED.has(j.status)) continue;
    if (seen.has(j.service_id)) continue;
    seen.add(j.service_id);
    if (j.status !== "canceled") ids.add(j.id);
  }
  return ids;
}

export function JobsScreen() {
  const [openLog, setOpenLog] = useState<number | null>(null);
  const [liveJob, setLiveJob] = useState<number | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const jobs = useJobs(100);
  const clear = useClearJobs();
  const rollback = useRollback();

  // Only terminal jobs are clearable; the backend keeps queued/running ones, so
  // the button stays disabled when there is nothing it could remove.
  const finishedCount = (jobs.data ?? []).filter((j) => FINISHED.has(j.status)).length;
  const rollbackable = latestApplyIds(jobs.data ?? []);

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-lg font-semibold">Jobs</h1>
        <Button
          variant="outline"
          disabled={finishedCount === 0 || clear.isPending}
          onClick={() => setConfirmClear(true)}
        >
          Clear finished
        </Button>
      </div>

      <Dialog open={confirmClear} onOpenChange={setConfirmClear}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Clear finished jobs?</DialogTitle>
            <DialogDescription>
              Deletes {finishedCount} finished job{finishedCount === 1 ? "" : "s"} and their logs.
              Queued and running jobs are kept. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmClear(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={clear.isPending}
              onClick={() => clear.mutate(undefined, { onSettled: () => setConfirmClear(false) })}
            >
              Clear
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {jobs.isLoading && (
        <div className="space-y-2" role="status" aria-label="Loading jobs">
          {Array.from({ length: 6 }).map((_, i) => (
            <div key={i} className="h-9 animate-pulse rounded-md bg-muted" />
          ))}
        </div>
      )}
      {jobs.isError && <p className="text-sm text-danger">Failed to load jobs.</p>}
      {!jobs.isLoading && !jobs.isError && (jobs.data ?? []).length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          No jobs have run yet.
        </div>
      )}
      {!jobs.isLoading && !jobs.isError && (jobs.data ?? []).length > 0 && (
        <div className="overflow-hidden rounded-lg border border-border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>#</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Scope</TableHead>
                <TableHead>Requested by</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(jobs.data ?? []).map((j) => (
                <TableRow key={j.id}>
                  <TableCell>{j.id}</TableCell>
                  <TableCell>{j.type}</TableCell>
                  <TableCell><Badge variant={STATUS_VARIANT[j.status] ?? "default"}>{j.status}</Badge></TableCell>
                  <TableCell>{j.scope}</TableCell>
                  <TableCell>{j.requested_by}</TableCell>
                  <TableCell>{new Date(j.created_at).toLocaleString()}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <button
                        type="button"
                        className="text-xs text-primary hover:underline"
                        onClick={() => setOpenLog(j.id)}
                      >
                        View log
                      </button>
                      {rollbackable.has(j.id) && (
                        <button
                          type="button"
                          className="text-xs text-danger hover:underline disabled:opacity-50"
                          disabled={rollback.isPending || liveJob !== null}
                          title="Restore this service to its pre-apply snapshot"
                          onClick={async () => {
                            const res = await rollback.mutateAsync(j.id);
                            setOpenLog(null);
                            setLiveJob(res.job_id);
                          }}
                        >
                          Rollback
                        </button>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {openLog !== null && liveJob === null && (
        <ApplyPanel key={openLog} jobId={openLog} readOnly onClose={() => setOpenLog(null)} />
      )}
      {/* Live (non-readOnly) panel for a rollback started from this screen:
          streams the log, shows "Rolling back" / "Rolled back", auto-closes. */}
      {liveJob !== null && <ApplyPanel key={liveJob} jobId={liveJob} onClose={() => setLiveJob(null)} />}
    </div>
  );
}
