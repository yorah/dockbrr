import { useEffect, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Check, Loader2, X } from "lucide-react";
import { jobQueryOptions } from "@/hooks/queries";
import { JobLogView } from "@/components/JobLogView";
import { Button } from "@/components/ui/button";
import type { AppliedJob, Job } from "@/api/types";

const TERMINAL = new Set(["success", "failed", "canceled"]);
const FAILED = new Set(["failed", "canceled"]);
const AUTO_CLOSE_SUCCESS_MS = 4000;

export interface BulkApplyPanelProps {
  jobs: AppliedJob[];
  // serviceId -> display name, resolved by the route from its cached projects.
  serviceNames: Map<number, string>;
  onClose: () => void;
}

function StatusIcon({ status }: { status?: string }) {
  if (status === "success") return <Check className="h-4 w-4 text-success" aria-label="success" />;
  if (status && FAILED.has(status)) return <X className="h-4 w-4 text-danger" aria-label="failed" />;
  return <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-label="running" />;
}

function JobRow({ job, name, data, defaultOpen }: { job: AppliedJob; name: string; data?: Job; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const status = data?.status;
  return (
    <li className="border-t border-border first:border-t-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 py-2 text-left text-sm"
      >
        {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        <span className="font-medium">{name}</span>
        <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          {status ?? "queued"}
          <StatusIcon status={status} />
        </span>
      </button>
      {open && (
        <div className="pb-3 pl-6">
          <JobLogView jobId={job.jobId} autoClose={false} />
        </div>
      )}
    </li>
  );
}

// Live panel for a batch apply (2+ jobs). Polls every original apply job for the
// aggregate + auto-close decision; each row expands to its own JobLogView (log +
// in-place rollback). Auto-closes only when EVERY apply succeeded.
export function BulkApplyPanel({ jobs, serviceNames, onClose }: BulkApplyPanelProps) {
  const results = useQueries({ queries: jobs.map((j) => jobQueryOptions(j.jobId)) });
  const statuses = results.map((r) => (r.data as Job | undefined)?.status);
  const done = statuses.filter((s) => s && TERMINAL.has(s)).length;
  const failed = statuses.filter((s) => s && FAILED.has(s)).length;
  const allSucceeded = jobs.length > 0 && statuses.every((s) => s === "success");

  // Auto-expand the first still-running row (else the first row) so a live log
  // shows immediately, fixing the "sat on a queued job" symptom.
  const firstRunningIndex = Math.max(0, statuses.findIndex((s) => s === "running"));

  useEffect(() => {
    if (!allSucceeded) return;
    const t = setTimeout(onClose, AUTO_CLOSE_SUCCESS_MS);
    return () => clearTimeout(t);
  }, [allSucceeded, onClose]);

  return (
    <section
      aria-label="Apply progress"
      className="fixed inset-x-0 bottom-0 z-40 mx-auto w-full max-w-3xl rounded-t-lg border border-border bg-card p-4 shadow-lg"
    >
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-medium">
          Applying {jobs.length} update{jobs.length > 1 ? "s" : ""} — {done}/{jobs.length} done, {failed} failed
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close apply panel">
          Close
        </Button>
      </header>
      <ul className="max-h-80 overflow-auto">
        {jobs.map((j, i) => (
          <JobRow key={j.jobId} job={j} name={serviceNames.get(j.serviceId) ?? `service #${j.serviceId}`} data={results[i].data as Job | undefined} defaultOpen={i === firstRunningIndex} />
        ))}
      </ul>
    </section>
  );
}
