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
import { useClearJobs } from "@/hooks/mutations";

const STATUS_VARIANT: Record<string, "default" | "success" | "warning" | "danger" | "info"> = {
  success: "success", failed: "danger", canceled: "default", running: "info", queued: "warning",
};

const FINISHED = new Set(["success", "failed", "canceled"]);

export function JobsScreen() {
  const [openLog, setOpenLog] = useState<number | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const jobs = useJobs(100);
  const clear = useClearJobs();

  // Only terminal jobs are clearable; the backend keeps queued/running ones, so
  // the button stays disabled when there is nothing it could remove.
  const finishedCount = (jobs.data ?? []).filter((j) => FINISHED.has(j.status)).length;

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
                <TableHead>Log</TableHead>
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
                    <button
                      type="button"
                      className="text-xs text-primary hover:underline"
                      onClick={() => setOpenLog(j.id)}
                    >
                      View
                    </button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {openLog !== null && <ApplyPanel key={openLog} jobId={openLog} readOnly onClose={() => setOpenLog(null)} />}
    </div>
  );
}
