import { useEffect, useState } from "react";
import { Download, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { notify } from "@/lib/notify";
import { Button, buttonVariants } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useSelfUpdate, useJob } from "@/hooks/queries";
import { useApplySelfUpdate } from "@/hooks/mutations";
import { useDismissedUpdate } from "@/hooks/useDismissedUpdate";
import { SELF_UPDATE_CONFIRM } from "@/lib/selfUpdate";

export { DISMISS_KEY } from "@/hooks/useDismissedUpdate";

/**
 * UpdateNotice shows a dismissable "Update Available" card in the sidebar when a
 * newer stable dockbrr release exists. Dismissal is per-version: hiding v0.5.0
 * keeps it hidden until a newer tag ships. In the collapsed sidebar it shrinks
 * to an icon-only link, mirroring the collapsed Logout button.
 */
export function UpdateNotice({ collapsed }: { collapsed: boolean }) {
  const { data } = useSelfUpdate();
  const apply = useApplySelfUpdate();
  const { dismissed, dismiss: setDismissed } = useDismissedUpdate();

  // The self_update job is enqueue-then-restart: POST returns an id instantly,
  // but the image pull + container swap run async and can outlive the request.
  // Track the job so the button stays disabled until the job itself terminates
  // (not just until POST returns, which let a second click re-fire it), and so a
  // pull failure (e.g. the image for the new tag is not published yet) surfaces
  // instead of vanishing silently.
  const [jobId, setJobId] = useState<number | null>(null);
  const job = useJob(jobId ?? 0, jobId !== null);
  const status = job.data?.status;
  const applying =
    apply.isPending ||
    (jobId !== null && status !== "success" && status !== "failed" && status !== "canceled");

  useEffect(() => {
    if (status !== "failed" && status !== "canceled") return;
    notify.error(
      job.data?.error
        ? `Update failed: ${job.data.error}`
        : "Update failed. The new image may not be published yet, try again in a few minutes.",
    );
    setJobId(null); // clear so the button re-enables for a retry
  }, [status, job.data?.error]);

  if (!data?.update_available) return null;
  if (dismissed === data.latest) return null;

  const dismiss = () => {
    // Guarded by the update_available check above: the backend always sends
    // latest/html_url together with update_available:true.
    setDismissed(data.latest!);
  };

  if (collapsed) {
    return (
      <div className="px-2">
        <Tooltip>
          <TooltipTrigger asChild>
            <a
              href={data.html_url}
              target="_blank"
              rel="noreferrer"
              aria-label={`Update available: ${data.latest}`}
              className="flex h-9 items-center justify-center rounded-md text-success transition-colors hover:bg-accent"
            >
              <Download className="h-4 w-4 shrink-0" />
            </a>
          </TooltipTrigger>
          <TooltipContent side="right">Update available: {data.latest}</TooltipContent>
        </Tooltip>
      </div>
    );
  }

  return (
    <div className="px-2">
      <div className="rounded-md border border-success/40 bg-success/10 p-3">
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2 text-sm font-medium text-success">
            <Download className="h-4 w-4 shrink-0" />
            <span>Update Available</span>
          </div>
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss update notification"
            className="text-muted-foreground transition-colors hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          Version {data.latest} is now available
        </p>
        <div className="mt-2 flex items-center gap-2">
          <Button
            type="button"
            variant="default"
            size="sm"
            disabled={applying}
            onClick={() => {
              if (window.confirm(SELF_UPDATE_CONFIRM)) {
                apply.mutate(undefined, { onSuccess: (res) => setJobId(res.job_id) });
              }
            }}
          >
            {applying ? "Updating..." : "Update now"}
          </Button>
          <a
            href={data.html_url}
            target="_blank"
            rel="noreferrer"
            className={cn(buttonVariants({ variant: "outline", size: "sm" }))}
          >
            View Release
          </a>
        </div>
      </div>
    </div>
  );
}
