import { ArrowUpCircle, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useApply, useProjectScan, useScanAll } from "@/hooks/mutations";
import { markServiceBusy, useBusyServices } from "@/hooks/useBusyServices";
import { useScanRun } from "@/hooks/useScanRun";
import type { AppliedJob, Update } from "@/api/types";

// Check every service in a project via a single scoped scan-run (POST
// /api/scan {project_id}), not a per-service fan-out. Disables while ANY
// scan-run is in flight (global, another project, or a single-row check),
// since the backend only allows one at a time; ScanProgress shows overall
// progress.
export function CheckAllButton({
  projectId,
  serviceIds,
  label = "Check all",
  ariaLabel,
}: {
  projectId: number;
  serviceIds: number[];
  label?: string;
  ariaLabel?: string;
}) {
  const scan = useProjectScan();
  const { running } = useScanRun();
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={serviceIds.length === 0 || running}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        if (serviceIds.length === 0) return;
        scan.mutate(projectId);
      }}
    >
      <RefreshCw className={running ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
      {label}
    </Button>
  );
}

// Run a full detection sweep (POST /api/scan), the dashboard-wide "Check
// all". Also stamps last_check_all and pushes a "scanned" SSE hint, so the
// "Last scan" tile updates immediately instead of waiting for its 60s poll or
// a page reload. Disables while any scan-run is in flight.
export function ScanAllButton({
  label = "Check all",
  ariaLabel,
}: {
  label?: string;
  ariaLabel?: string;
}) {
  const scan = useScanAll();
  const { running } = useScanRun();
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={running}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        scan.mutate();
      }}
    >
      <RefreshCw className={running ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
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
  onApplied: (jobs: AppliedJob[]) => void;
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
      onClick={async (e) => {
        e.stopPropagation();
        if (pending.length === 0) return;
        const n = pending.length;
        const anySelf = pending.some((u) => u.is_self);
        const base = `Apply ${n} available update${n > 1 ? "s" : ""} ${scopeNoun}? Each affected service is recreated individually.`;
        const msg = anySelf
          ? `${base} This includes dockbrr itself, which will restart and briefly disconnect this page.`
          : base;
        if (!window.confirm(msg)) return;
        // Enqueue every apply, then report the full set once so the panel can
        // list all jobs. allSettled: one failed enqueue POST must not drop the
        // rest. Order is preserved (map index == pending index).
        const results = await Promise.allSettled(
          pending.map((u) => apply.mutateAsync({ id: u.id, scope: "service" })),
        );
        const jobs: AppliedJob[] = [];
        results.forEach((r, i) => {
          if (r.status !== "fulfilled") return;
          const serviceId = pending[i].service_id;
          markServiceBusy(serviceId, r.value.job_id, "apply");
          jobs.push({ jobId: r.value.job_id, serviceId });
        });
        if (jobs.length > 0) onApplied(jobs);
      }}
    >
      <ArrowUpCircle className="h-3.5 w-3.5" />
      {label}
    </Button>
  );
}
