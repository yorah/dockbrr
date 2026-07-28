import { useEffect, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Check, Clock, Loader2, X } from "lucide-react";
import { jobQueryOptions, TERMINAL_JOB_STATUSES, FAILED_JOB_STATUSES } from "@/hooks/queries";
import { JobLogView } from "@/components/JobLogView";
import { Button } from "@/components/ui/button";
import type { AppliedJob, Job } from "@/api/types";

const AUTO_CLOSE_SUCCESS_MS = 4000;

export interface BulkApplyPanelProps {
  jobs: AppliedJob[];
  // serviceId -> display name, resolved by the route from its cached projects.
  serviceNames: Map<number, string>;
  onClose: () => void;
}

// queued is the pre-run state (no spinner: nothing is happening yet), running
// gets the spinner, terminal states get a glyph. Wording matches the row label
// rather than the raw status so a collapsed row reads as progress.
function StatusIcon({ status }: { status?: string }) {
  if (status === "success") return <Check className="h-4 w-4 text-success" aria-label="success" />;
  if (status && FAILED_JOB_STATUSES.has(status)) return <X className="h-4 w-4 text-danger" aria-label="failed" />;
  if (status === "running") return <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-label="running" />;
  return <Clock className="h-4 w-4 text-muted-foreground" aria-label="queued" />;
}

function statusLabel(status?: string) {
  if (status === "success") return "applied";
  if (status === "running") return "applying…";
  return status ?? "queued";
}

function JobRow({ job, name, data, open, onToggle }: { job: AppliedJob; name: string; data?: Job; open: boolean; onToggle: () => void }) {
  const status = data?.status;
  return (
    <li className="border-t border-border first:border-t-0">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-2 py-2 text-left text-sm"
      >
        {open ? <ChevronDown className="h-4 w-4 shrink-0" /> : <ChevronRight className="h-4 w-4 shrink-0" />}
        <span className="truncate font-medium">{name}</span>
        <span className="ml-auto flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
          {statusLabel(status)}
          <StatusIcon status={status} />
        </span>
      </button>
      {open && (
        <div className="pb-3 pl-6">
          {/* Fixed log height (not max-h): a growing log box inside the list's
              own scroll container makes both scrollbars toggle on every
              streamed line. */}
          <JobLogView jobId={job.jobId} autoClose={false} logHeightClass="h-48" />
        </div>
      )}
    </li>
  );
}

// Live panel for a batch apply (2+ jobs). Polls every original apply job for the
// aggregate + auto-close decision. Auto-closes only when EVERY apply succeeded.
//
// Every row starts COLLAPSED and stays that way until clicked: the batch view's
// job is progress (per-row status + the header bar), and an expanded row's
// streaming log inside the list's scroll container makes the layout thrash.
// Expanding a row mounts its JobLogView (log + in-place rollback) and its SSE
// subscription, so collapsed rows also cost nothing.
export function BulkApplyPanel({ jobs, serviceNames, onClose }: BulkApplyPanelProps) {
  const results = useQueries({ queries: jobs.map((j) => jobQueryOptions(j.jobId)) });
  const statuses = results.map((r) => (r.data as Job | undefined)?.status);
  const done = statuses.filter((s) => s && TERMINAL_JOB_STATUSES.has(s)).length;
  const failed = statuses.filter((s) => s && FAILED_JOB_STATUSES.has(s)).length;
  const allSucceeded = jobs.length > 0 && statuses.every((s) => s === "success");

  const [openRows, setOpenRows] = useState<Set<number>>(new Set());

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
      <header className="mb-2 flex items-center justify-between gap-4">
        <h2 className="text-sm font-medium">
          Applying {jobs.length} update{jobs.length > 1 ? "s" : ""} · {done}/{jobs.length} done, {failed} failed
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close apply panel">
          Close
        </Button>
      </header>
      {/* Batch progress: the collapsed rows' only aggregate signal. */}
      <div
        role="progressbar"
        aria-label="Batch progress"
        aria-valuemin={0}
        aria-valuemax={jobs.length}
        aria-valuenow={done}
        className="mb-3 h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <div
          className={`h-full transition-[width] duration-300 ${failed > 0 ? "bg-danger" : "bg-success"}`}
          style={{ width: `${jobs.length === 0 ? 0 : (done / jobs.length) * 100}%` }}
        />
      </div>
      {/* overflow-y only + a stable gutter: a toggling scrollbar must not change
          the content width, or the mono log inside an open row reflows. */}
      <ul className="max-h-80 overflow-y-auto [scrollbar-gutter:stable]">
        {jobs.map((j, i) => (
          <JobRow
            key={j.jobId}
            job={j}
            name={serviceNames.get(j.serviceId) ?? `service #${j.serviceId}`}
            data={results[i].data as Job | undefined}
            open={openRows.has(j.jobId)}
            onToggle={() =>
              setOpenRows((prev) => {
                const n = new Set(prev);
                if (n.has(j.jobId)) n.delete(j.jobId);
                else n.add(j.jobId);
                return n;
              })
            }
          />
        ))}
      </ul>
    </section>
  );
}
