import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useJobLog } from "@/hooks/useJobLog";
import { useJob } from "@/hooks/queries";
import { keys } from "@/api/keys";
import { RollbackButton } from "@/components/RollbackButton";
import { Button } from "@/components/ui/button";

export interface ApplyPanelProps {
  jobId: number;
  onClose: () => void;
  // readOnly renders the panel as a pure log viewer (no RollbackButton), used by
  // the service history screen to inspect a past job without offering a mutating
  // action that wouldn't correspond to the historical event on screen.
  readOnly?: boolean;
}

// The backend job status vocabulary (store/jobs.go) is queued|running|success|
// failed|canceled, the SAME string for a successful apply AND rollback. We
// disambiguate the success wording by the job's type. (succeeded/apply_failed/
// rolled_back are EVENT kinds, not job statuses, so do not key off them here.)
const FAILED_STATUSES = new Set(["failed", "canceled"]);
const TERMINAL_STATUSES = new Set(["success", "failed", "canceled"]);
const AUTO_CLOSE_SUCCESS_MS = 4000;

// Live-panel title per job type (store/jobs.go type vocabulary). The panel
// hosts every job-backed action, not just applies.
const TITLES: Record<string, string> = {
  apply: "Applying update",
  rollback: "Rolling back",
  start: "Starting",
  stop: "Stopping",
  restart: "Restarting",
  remove: "Removing",
};

function panelTitle(readOnly: boolean, type?: string) {
  if (readOnly) return "Job log";
  return (type && TITLES[type]) || "Running job";
}

// Success wording per job type; rollback keeps its warning tone.
const SUCCESS_LABELS: Record<string, string> = {
  apply: "Applied",
  rollback: "Rolled back",
  start: "Started",
  stop: "Stopped",
  restart: "Restarted",
  remove: "Removed",
};

function StatusLine({ status, type, error, closingIn }: { status?: string; type?: string; error?: string; closingIn?: number }) {
  if (status === "success") {
    const suffix = closingIn !== undefined ? ` · closing in ${closingIn}s` : "";
    const label = (type && SUCCESS_LABELS[type]) || "Done";
    if (type === "rollback") {
      return <p className="text-sm font-medium text-warning">{label}{suffix}</p>;
    }
    return <p className="text-sm font-medium text-success">{label}{suffix}</p>;
  }
  if (status && FAILED_STATUSES.has(status)) {
    return (
      <p role="alert" className="text-sm font-medium text-danger">
        {error || (status === "canceled" ? "Canceled" : "Job failed")}
      </p>
    );
  }
  return <p className="text-sm text-muted-foreground">Health gate: waiting…</p>;
}

export function ApplyPanel({ jobId: initialJobId, onClose, readOnly = false }: ApplyPanelProps) {
  // Internal state so a rollback can swap this panel to the new job id in place.
  const [jobId, setJobId] = useState(initialJobId);
  const { lines } = useJobLog(jobId);
  const job = useJob(jobId, true);
  const status = job.data?.status;
  const jobType = job.data?.type;
  const logRef = useRef<HTMLDivElement>(null);
  const qc = useQueryClient();

  // Auto-scroll to the bottom as new lines stream in.
  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  // When a live apply/rollback reaches a terminal status, refresh the dashboard
  // data locally. SSE (useEventStream) normally pushes this on job_finished, but
  // a dropped/reconnecting stream would otherwise leave the list stale until the
  // next focus refetch, and this closes that freshness gap. Skipped in readOnly
  // (history viewer) where the job is already in the past.
  useEffect(() => {
    if (readOnly || !status || !TERMINAL_STATUSES.has(status)) return;
    void qc.invalidateQueries({ queryKey: keys.projects });
    void qc.invalidateQueries({ queryKey: keys.updates });
    void qc.invalidateQueries({ queryKey: keys.jobs });
  }, [readOnly, status, jobId, qc]);

  // A successful job dismisses the panel on its own after a beat: long enough
  // to read the success line, short enough that start/stop (which open this
  // panel too) don't leave it parked over the table. Failures stay open, the
  // error and the rollback offer must not vanish out from under the user.
  // closingIn drives the visible "closing in Ns" countdown on the status line.
  const [closingIn, setClosingIn] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (readOnly || status !== "success") return;
    setClosingIn(AUTO_CLOSE_SUCCESS_MS / 1000);
    const tick = setInterval(
      () => setClosingIn((s) => (s !== undefined && s > 1 ? s - 1 : s)),
      1000,
    );
    const t = setTimeout(onClose, AUTO_CLOSE_SUCCESS_MS);
    return () => {
      clearTimeout(t);
      clearInterval(tick);
    };
  }, [readOnly, status, onClose]);

  return (
    <section
      aria-label="Apply progress"
      className="fixed inset-x-0 bottom-0 z-40 mx-auto w-full max-w-3xl rounded-t-lg border border-border bg-card p-4 shadow-lg"
    >
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-medium">
          {panelTitle(readOnly, jobType)} (job #{jobId})
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close apply panel">
          Close
        </Button>
      </header>

      <StatusLine status={status} type={jobType} error={job.data?.error} closingIn={closingIn} />

      <div
        ref={logRef}
        data-testid="apply-log"
        className="mt-2 max-h-64 overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed text-foreground"
      >
        {lines.length === 0 ? (
          <p className="opacity-60">Waiting for log output…</p>
        ) : (
          lines.map((l, i) => (
            <div key={i} className={l.stream === "stderr" ? "text-danger" : undefined}>
              {l.line}
            </div>
          ))
        )}
      </div>

      {!readOnly && status && FAILED_STATUSES.has(status) && (
        <div className="mt-3 flex justify-end">
          <RollbackButton originalJobId={jobId} onRollback={setJobId} />
        </div>
      )}
    </section>
  );
}
