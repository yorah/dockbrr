import { Link } from "@tanstack/react-router";
import { LayoutDashboard, ListChecks, Settings } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const ITEMS: Array<{ to: string; label: string; icon: LucideIcon; exact?: boolean }> = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, exact: true },
  { to: "/jobs", label: "Jobs", icon: ListChecks },
  { to: "/settings", label: "Settings", icon: Settings },
];

/** Shared row styling for every sidebar entry (nav items and project rows). */
export const rowClass =
  "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground";
export const rowActiveClass =
  "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm font-medium bg-primary text-primary-foreground hover:bg-primary";

export function SidebarNav({ collapsed }: { collapsed: boolean }) {
  return (
    <nav className="flex flex-col gap-1 px-2" aria-label="Main">
      {ITEMS.map(({ to, label, icon: Icon, exact }) => {
        const link = (
          <Link
            key={to}
            to={to}
            className={cn(rowClass, collapsed && "justify-center px-0")}
            activeProps={{ className: cn(rowActiveClass, collapsed && "justify-center px-0") }}
            activeOptions={exact ? { exact: true } : undefined}
            aria-label={collapsed ? label : undefined}
          >
            <Icon className="h-4 w-4 shrink-0" />
            {!collapsed && <span className="truncate">{label}</span>}
          </Link>
        );
        if (!collapsed) return link;
        return (
          <Tooltip key={to}>
            <TooltipTrigger asChild>{link}</TooltipTrigger>
            <TooltipContent side="right">{label}</TooltipContent>
          </Tooltip>
        );
      })}
    </nav>
  );
}
