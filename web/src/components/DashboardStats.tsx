import type { LucideIcon } from "lucide-react";
import { AlertTriangle, ArrowUpCircle, Boxes, CircleStop, Clock, Pin } from "lucide-react";
import type { Project, Update, SystemStatus } from "@/api/types";
import { cn } from "@/lib/cn";
import { relative, until } from "@/components/RelativeTime";
import { isStopped } from "@/components/StatusBadge";
import type { Row } from "@/hooks/useDashboardRows";
import { useNow } from "@/hooks/useNow";

export interface DashboardStatsProps {
  projects: Project[];
  updates: Update[];
  // The same filtered/joined rows the table renders. When provided, the
  // "Updates available" count only considers services currently visible
  // under the active filter (e.g. a Gone service's update isn't counted
  // while show-removed is off), so the tile matches what clicking it reveals.
  // Falls back to all services when omitted.
  rows?: Row[];
  status?: SystemStatus;
  // The status filter currently applied by the table, so the matching tile can
  // render as selected. "" means "Any status", so the Services tile is selected.
  activeStatus?: string;
  onFilter: (patch: { status?: string }) => void;
}

function Tile({
  label,
  value,
  sub,
  icon: Icon,
  tone,
  onClick,
  selected,
}: {
  label: string;
  value: string | number;
  // Optional third line under the label, for context the value can't carry.
  sub?: string;
  icon?: LucideIcon;
  tone?: string;
  onClick?: () => void;
  selected?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      aria-pressed={onClick ? selected : undefined}
      className={cn(
        "relative flex min-w-36 flex-col items-start rounded-lg border border-border bg-card px-4 py-3 text-left transition-colors enabled:hover:bg-accent",
        selected && "ring-2 ring-ring",
      )}
    >
      {Icon && <Icon className="absolute top-3 right-3 h-4 w-4 text-muted-foreground" aria-hidden />}
      {/* pr-6 reserves the icon's corner: a wide value ("11m ago") ran under it. */}
      <span className={cn("pr-6 text-2xl font-semibold tabular-nums", tone)}>{value}</span>
      <span className="mt-0.5 text-xs text-muted-foreground">{label}</span>
      {sub && <span className="mt-0.5 text-[11px] text-muted-foreground/70 tabular-nums">{sub}</span>}
    </button>
  );
}

export function DashboardStats({ projects, updates, rows, status, activeStatus = "", onFilter }: DashboardStatsProps) {
  // The scan tile's two clock readings ("11m ago", "next in 4m") must age between
  // the status query's 60s polls, so re-render on a tick of our own.
  const now = useNow(10_000);
  const nextIn = status?.next_check_all ? until(status.next_check_all, now) : "";
  const services = projects.flatMap((p) => p.services);
  const visibleServiceIds = rows
    ? new Set(rows.filter((r) => r.kind === "service").map((r) => r.service.id))
    : new Set(services.map((s) => s.id));
  const open = updates.filter((u) => u.status === "available" && visibleServiceIds.has(u.service_id));
  const pinned = services.filter((s) => s.pinned);
  const stopped = services.filter((s) => isStopped(s.state));
  const gone = services.filter((s) => s.state === "gone");

  // Second click on the already-active status tile reverts to "Any status"
  // (which also re-selects the Services tile).
  const toggle = (s: string) => onFilter({ status: activeStatus === s ? "" : s });

  return (
    <div className="mb-4 flex flex-wrap gap-3">
      <Tile
        label="Services"
        value={services.length}
        // The count includes Gone services even though they're hidden from
        // the table unless "Show removed" is on. Without this, the tile
        // promises services that the table then appears to lose.
        sub={gone.length > 0 ? `${gone.length} removed` : undefined}
        icon={Boxes}
        selected={activeStatus === ""}
        onClick={() => onFilter({ status: "" })}
      />
      <Tile
        label="Updates available"
        value={open.length}
        icon={ArrowUpCircle}
        tone={open.length > 0 ? "text-warning" : undefined}
        selected={activeStatus === "update-available"}
        onClick={() => toggle("update-available")}
      />
      <Tile
        label="Pinned"
        value={pinned.length}
        icon={Pin}
        selected={activeStatus === "pinned"}
        onClick={() => toggle("pinned")}
      />
      <Tile
        label="Stopped"
        value={stopped.length}
        icon={CircleStop}
        tone={stopped.length > 0 ? "text-danger" : undefined}
        selected={activeStatus === "stopped"}
        onClick={() => toggle("stopped")}
      />
      <Tile
        label="Last scan"
        value={status?.last_check_all ? relative(status.last_check_all, now) : "-"}
        sub={nextIn ? `next in ${nextIn}` : undefined}
        icon={Clock}
      />
      {status && !status.docker_reachable && (
        <Tile label="Docker" value="unreachable" icon={AlertTriangle} tone="text-danger" />
      )}
    </div>
  );
}
