import { useState } from "react";
import { Download, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button, buttonVariants } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useSelfUpdate } from "@/hooks/queries";
import { useApplySelfUpdate } from "@/hooks/mutations";

export const DISMISS_KEY = "dockbrr_dismissed_update";

/**
 * UpdateNotice shows a dismissable "Update Available" card in the sidebar when a
 * newer stable dockbrr release exists. Dismissal is per-version: hiding v0.5.0
 * keeps it hidden until a newer tag ships. In the collapsed sidebar it shrinks
 * to an icon-only link, mirroring the collapsed Logout button.
 */
export function UpdateNotice({ collapsed }: { collapsed: boolean }) {
  const { data } = useSelfUpdate();
  const apply = useApplySelfUpdate();
  const [dismissed, setDismissed] = useState<string | null>(() => localStorage.getItem(DISMISS_KEY));

  if (!data?.update_available) return null;
  if (dismissed === data.latest) return null;

  const dismiss = () => {
    // Guarded by the update_available check above: the backend always sends
    // latest/html_url together with update_available:true.
    localStorage.setItem(DISMISS_KEY, data.latest!);
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
            disabled={apply.isPending}
            onClick={() => apply.mutate()}
          >
            {apply.isPending ? "Updating..." : "Update now"}
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
