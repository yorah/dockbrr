import { ArrowUpCircle, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useApply, useCheckAll, useScanAll } from "@/hooks/mutations";
import { markServiceBusy, useBusyServices } from "@/hooks/useBusyServices";
import type { Update } from "@/api/types";

// Check every service in the given set. Detection is read-only (never touches
// Docker), so no confirm, just one click that fans out per-service checks.
// Scoped use (e.g. one project); for the dashboard-wide sweep use ScanAllButton,
// which also refreshes the "Last scan" tile.
export function CheckAllButton({
  serviceIds,
  label = "Check all",
  ariaLabel,
}: {
  serviceIds: number[];
  label?: string;
  ariaLabel?: string;
}) {
  const check = useCheckAll();
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={serviceIds.length === 0 || check.isPending}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        if (serviceIds.length === 0) return;
        check.mutate(serviceIds);
      }}
    >
      <RefreshCw className={check.isPending ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
      {label}
    </Button>
  );
}

// Run a full detection sweep (POST /api/scan), the dashboard-wide "Check
// all". Unlike CheckAllButton's per-service fan-out, this also stamps
// last_check_all and pushes a "scanned" SSE hint, so the "Last scan" tile
// updates immediately instead of waiting for its 60s poll or a page reload.
export function ScanAllButton({
  label = "Check all",
  ariaLabel,
}: {
  label?: string;
  ariaLabel?: string;
}) {
  const scan = useScanAll();
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={scan.isPending}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        scan.mutate();
      }}
    >
      <RefreshCw className={scan.isPending ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
      {label}
    </Button>
  );
}

// Apply every available update in the given set. Enqueues one SERVICE-scope
// apply per update (never a project-scope `up`, which would recreate whole
// stacks and revert siblings applied via a non-persistent pin override). The
// per-project mutex serializes the jobs; the live panel opens on the first.
export function ApplyAllButton({
  updates,
  onApplied,
  scopeNoun,
  label = "Apply all",
  ariaLabel,
}: {
  updates: Update[];
  onApplied: (jobId: number) => void;
  /** Trailing phrase for the confirm, e.g. `in "app"` or `across all projects`. */
  scopeNoun: string;
  label?: string;
  ariaLabel?: string;
}) {
  const apply = useApply();
  // Job-backed per-service busy state (shared with the row buttons via the
  // same store), so Apply-all disables while any of ITS services still has
  // an apply/start/stop/restart job in flight, and re-enables once every one
  // clears rather than immediately after the (sub-second) enqueue POSTs.
  const busyMap = useBusyServices();
  const pending = updates.filter((u) => u.status === "available");
  const anyBusy = pending.some((u) => busyMap.has(u.service_id));
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={pending.length === 0 || apply.isPending || anyBusy}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        if (pending.length === 0) return;
        const n = pending.length;
        if (!window.confirm(`Apply ${n} available update${n > 1 ? "s" : ""} ${scopeNoun}? Each affected service is recreated individually.`)) return;
        let opened = false;
        for (const u of pending) {
          apply.mutate(
            { id: u.id, scope: "service" },
            {
              onSuccess: (res) => {
                markServiceBusy(u.service_id, res.job_id, "apply");
                if (!opened) {
                  opened = true;
                  onApplied(res.job_id);
                }
              },
            },
          );
        }
      }}
    >
      <ArrowUpCircle className="h-3.5 w-3.5" />
      {label}
    </Button>
  );
}
