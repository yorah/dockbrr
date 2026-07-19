import { Link, Outlet } from "@tanstack/react-router";
import {
  Database,
  FileText,
  Info,
  Radar,
  RefreshCw,
  Shield,
  Zap,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/cn";
import { rowActiveClass, rowClass } from "@/components/layout/SidebarNav";

const SECTIONS = [
  { to: "/settings/application", label: "Application", icon: Info },
  { to: "/settings/scanning", label: "Scanning", icon: Radar },
  { to: "/settings/updates", label: "Updates", icon: RefreshCw },
  { to: "/settings/auto-update", label: "Auto-update", icon: Zap },
  { to: "/settings/registries", label: "Registries", icon: Database },
  { to: "/settings/security", label: "Security", icon: Shield },
  { to: "/settings/logs", label: "Logs", icon: FileText },
] as const satisfies ReadonlyArray<{ to: string; label: string; icon: LucideIcon }>;

/**
 * Settings shell: a section nav plus the active section. The nav reuses the app
 * sidebar's row classes so an active settings section reads identically to an
 * active app nav item. Below `md` it becomes a horizontal scroller, since a second
 * vertical rail is unusable on a phone.
 */
export function SettingsLayout() {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 md:flex-row">
      <nav
        aria-label="Settings sections"
        // The sibling content column scrolls, not this nav, so it stays put
        // on its own (no sticky needed).
        className="flex shrink-0 gap-1 overflow-x-auto md:w-56 md:flex-col md:overflow-x-visible"
      >
        {SECTIONS.map(({ to, label, icon: Icon }) => (
          <Link
            key={to}
            to={to}
            className={cn(rowClass, "w-auto whitespace-nowrap md:w-full")}
            activeProps={{ className: cn(rowActiveClass, "w-auto whitespace-nowrap md:w-full") }}
          >
            <Icon className="h-4 w-4 shrink-0" />
            <span>{label}</span>
          </Link>
        ))}
      </nav>
      {/* The content column is its own scroll region (md+), so the scrollbar
          hugs the cards instead of running down the whole pane; md:pr-4 keeps
          the cards a step off the thumb. Below md the page scrolls as one. */}
      <div className="min-h-0 min-w-0 flex-1 md:overflow-y-auto md:pr-4">
        <Outlet />
      </div>
    </div>
  );
}
