import { useScanRun } from "@/hooks/useScanRun";

// A determinate, self-contained progress indicator for the in-flight scan-run.
// Renders nothing when idle. Shared, server-authoritative state (useScanRun)
// means it reflects a scan started on any page, including after navigation.
export function ScanProgress() {
  const { running, done, total } = useScanRun();
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
    </div>
  );
}
