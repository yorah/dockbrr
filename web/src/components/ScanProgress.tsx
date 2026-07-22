import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useScanRun } from "@/hooks/useScanRun";
import { useScanAbort } from "@/hooks/mutations";

// A determinate, self-contained progress indicator for the in-flight scan-run.
// Renders nothing when idle. Shared, server-authoritative state (useScanRun)
// means it reflects a scan started on any page (manual OR scheduled), including
// after navigation. The Cancel button aborts the run (DELETE /api/scan); the
// bar then clears on the scan_finished the abort produces.
export function ScanProgress() {
  const { running, done, total } = useScanRun();
  const abort = useScanAbort();
  if (!running) return null;
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <div
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={done}
        aria-label="Checking services"
        className="h-1.5 w-28 overflow-hidden rounded-full bg-muted"
      >
        <div className="h-full bg-primary transition-[width]" style={{ width: `${pct}%` }} />
      </div>
      <span className="tabular-nums">
        Checking {done} / {total}
      </span>
      <Button
        size="sm"
        variant="ghost"
        className="h-6 gap-1 px-1.5"
        aria-label="Cancel scan"
        disabled={abort.isPending}
        onClick={() => abort.mutate()}
      >
        <X className="h-3.5 w-3.5" />
        Cancel
      </Button>
    </div>
  );
}
