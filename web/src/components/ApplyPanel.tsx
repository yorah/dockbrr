import { useState } from "react";
import { JobLogView } from "@/components/JobLogView";
import { Button } from "@/components/ui/button";
import { useJob } from "@/hooks/queries";

export interface ApplyPanelProps {
  jobId: number;
  onClose: () => void;
  // readOnly renders the panel as a pure log viewer (no RollbackButton), used by
  // the service history screen to inspect a past job without offering a mutating
  // action that wouldn't correspond to the historical event on screen.
  readOnly?: boolean;
}

// Live-panel title per job type (store/jobs.go type vocabulary). The panel
// hosts every job-backed action, not just applies.
const TITLES: Record<string, string> = {
  apply: "Applying update",
  rollback: "Rolling back",
  start: "Starting",
  stop: "Stopping",
  restart: "Restarting",
  remove: "Removing",
  self_update: "Updating dockbrr",
};

function panelTitle(readOnly: boolean, type?: string) {
  if (readOnly) return "Job log";
  return (type && TITLES[type]) || "Running job";
}

export function ApplyPanel({ jobId: initialJobId, onClose, readOnly = false }: ApplyPanelProps) {
  // Mirrors JobLogView's internal rollback swap (via onJobIdChange) so the
  // header's job number and title track the rollback job once one is
  // enqueued. Also used for the title's job type; JobLogView independently
  // calls useJob(jobId) for the body. Both share the same cache key
  // (keys.job(jobId)), so this is one network poll per job, not two.
  const [jobId, setJobId] = useState(initialJobId);
  const job = useJob(jobId, true);
  return (
    <section
      aria-label="Apply progress"
      className="fixed inset-x-0 bottom-0 z-40 mx-auto w-full max-w-3xl rounded-t-lg border border-border bg-card p-4 shadow-lg"
    >
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-medium">
          {panelTitle(readOnly, job.data?.type)} (job #{jobId})
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close apply panel">
          Close
        </Button>
      </header>
      <JobLogView
        jobId={initialJobId}
        readOnly={readOnly}
        autoClose={!readOnly}
        onClose={onClose}
        onJobIdChange={setJobId}
      />
    </section>
  );
}
