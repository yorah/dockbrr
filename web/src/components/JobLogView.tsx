import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useJobLog } from "@/hooks/useJobLog";
import { useJob, TERMINAL_JOB_STATUSES, FAILED_JOB_STATUSES } from "@/hooks/queries";
import { keys } from "@/api/keys";
import { RollbackButton } from "@/components/RollbackButton";

// The backend job status vocabulary (store/jobs.go) is queued|running|success|
// failed|canceled, the SAME string for a successful apply AND rollback. We
// disambiguate the success wording by the job's type. (succeeded/apply_failed/
// rolled_back are EVENT kinds, not job statuses, so do not key off them here.)
const AUTO_CLOSE_SUCCESS_MS = 4000;

// Success wording per job type; rollback keeps its warning tone.
const SUCCESS_LABELS: Record<string, string> = {
  apply: "Applied",
  rollback: "Rolled back",
  start: "Started",
  stop: "Stopped",
  restart: "Restarted",
  remove: "Removed",
  self_update: "Update started",
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
  if (status && FAILED_JOB_STATUSES.has(status)) {
    return (
      <p role="alert" className="text-sm font-medium text-danger">
        {error || (status === "canceled" ? "Canceled" : "Job failed")}
      </p>
    );
  }
  return <p className="text-sm text-muted-foreground">Health gate: waiting…</p>;
}

export interface JobLogViewProps {
  jobId: number;
  // readOnly renders a pure log viewer (no rollback, no invalidation): the
  // history/jobs screens inspect a past job.
  readOnly?: boolean;
  // When true, a successful job dismisses itself after AUTO_CLOSE_SUCCESS_MS
  // and calls onClose (single-job panel behavior). Bulk rows pass false; the
  // BulkApplyPanel owns the batch's lifecycle.
  autoClose?: boolean;
  onClose?: () => void;
  // Notified with the live jobId whenever a rollback swaps this view onto the
  // new job. JobLogView owns the swap itself (so a standalone/bulk row needs
  // no parent wiring); a chrome wrapper that displays the job number in its
  // own header (ApplyPanel) can mirror it via this callback.
  onJobIdChange?: (jobId: number) => void;
}

// One job's live status + log + in-place rollback. Extracted from ApplyPanel so
// both the single-job panel and each BulkApplyPanel row render identically.
export function JobLogView({ jobId: initialJobId, readOnly = false, autoClose = false, onClose, onJobIdChange }: JobLogViewProps) {
  // Internal state so a rollback can swap this view to the new job id in place.
  const [jobId, setJobId] = useState(initialJobId);
  const { lines } = useJobLog(jobId);
  const job = useJob(jobId, true);
  const status = job.data?.status;
  const jobType = job.data?.type;
  const logRef = useRef<HTMLDivElement>(null);
  const qc = useQueryClient();

  useEffect(() => {
    onJobIdChange?.(jobId);
  }, [jobId, onJobIdChange]);

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
    if (readOnly || !status || !TERMINAL_JOB_STATUSES.has(status)) return;
    void qc.invalidateQueries({ queryKey: keys.projects });
    void qc.invalidateQueries({ queryKey: keys.updates });
    void qc.invalidateQueries({ queryKey: keys.jobs });
  }, [readOnly, status, jobId, qc]);

  // A successful job dismisses itself on its own after a beat, when autoClose
  // is set: long enough to read the success line, short enough that start/stop
  // (which open the single-job panel too) don't leave it parked over the
  // table. Failures stay open, the error and the rollback offer must not
  // vanish out from under the user. closingIn drives the visible "closing in
  // Ns" countdown on the status line. State only changes inside the
  // interval/cleanup (never synchronously in the effect body,
  // react-hooks/set-state-in-effect); the first second's value is the
  // render-time default below.
  const [closingIn, setClosingIn] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!autoClose || readOnly || status !== "success") return;
    const full = AUTO_CLOSE_SUCCESS_MS / 1000;
    const tick = setInterval(
      () => setClosingIn((s) => (s === undefined ? full - 1 : s > 1 ? s - 1 : s)),
      1000,
    );
    const t = onClose ? setTimeout(onClose, AUTO_CLOSE_SUCCESS_MS) : undefined;
    return () => {
      if (t) clearTimeout(t);
      clearInterval(tick);
      setClosingIn(undefined);
    };
  }, [autoClose, readOnly, status, onClose]);
  const closingInShown = autoClose && status === "success" && !readOnly ? (closingIn ?? AUTO_CLOSE_SUCCESS_MS / 1000) : undefined;

  return (
    <>
      <StatusLine status={status} type={jobType} error={job.data?.error} closingIn={closingInShown} />
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
      {!readOnly && status && FAILED_JOB_STATUSES.has(status) && (
        <div className="mt-3 flex justify-end">
          <RollbackButton originalJobId={jobId} onRollback={setJobId} />
        </div>
      )}
    </>
  );
}
