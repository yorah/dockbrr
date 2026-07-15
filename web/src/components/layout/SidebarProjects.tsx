import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Plus } from "lucide-react";
import { cn } from "@/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useProjectHealth, type Dot } from "@/hooks/useProjectHealth";
import { rowActiveClass, rowClass } from "@/components/layout/SidebarNav";
import { AddProjectDialog } from "@/components/AddProjectDialog";
import type { Project } from "@/api/types";

const DOT_CLASS: Record<Dot, string> = {
  red: "bg-danger",
  amber: "bg-warning",
  green: "bg-success",
};
const DOT_LABEL: Record<Dot, string> = {
  red: "last job failed",
  amber: "updates available",
  green: "healthy",
};

export function SidebarProjects({ collapsed }: { collapsed: boolean }) {
  const { projects, health } = useProjectHealth();
  const [addOpen, setAddOpen] = useState(false);
  const [looseOpen, setLooseOpen] = useState(false);

  const named = projects.filter((p) => !p.auto_named);
  const loose = projects.filter((p) => p.auto_named);

  function renderProject(p: Project) {
    const h = health.get(p.id) ?? { updates: 0, dot: "green" as Dot };
    const label = `${p.name}, ${DOT_LABEL[h.dot]}`;
    const link = (
      <Link
        to="/project/$id"
        params={{ id: String(p.id) }}
        className={cn(rowClass, collapsed && "justify-center px-0")}
        activeProps={{ className: cn(rowActiveClass, collapsed && "justify-center px-0") }}
        aria-label={label}
      >
        {collapsed ? (
          <span aria-hidden className="text-xs font-semibold uppercase">
            {p.name.slice(0, 2)}
          </span>
        ) : (
          <span className="truncate">{p.name}</span>
        )}
        {!collapsed && h.updates > 0 && (
          <span className="ml-auto rounded-full bg-warning/15 px-1.5 py-0.5 text-xs font-medium text-warning tabular-nums">
            {h.updates}
          </span>
        )}
        <span
          aria-hidden
          className={cn(
            "h-2 w-2 shrink-0 rounded-full",
            DOT_CLASS[h.dot],
            !collapsed && h.updates === 0 && "ml-auto",
            collapsed && "absolute top-1 right-1",
          )}
        />
      </Link>
    );
    if (!collapsed) return <div key={p.id}>{link}</div>;
    return (
      <Tooltip key={p.id}>
        <TooltipTrigger asChild>
          <div className="relative">{link}</div>
        </TooltipTrigger>
        <TooltipContent side="right">
          {p.name}
          {h.updates > 0 && `, ${h.updates} update${h.updates === 1 ? "" : "s"}`}
        </TooltipContent>
      </Tooltip>
    );
  }

  return (
    <div className="flex min-h-0 flex-col">
      {!collapsed && (
        <div className="flex items-center justify-between px-3 py-2">
          <span className="text-xs font-medium tracking-wider text-muted-foreground uppercase">
            Projects
          </span>
          <button
            type="button"
            aria-label="Add project"
            onClick={() => setAddOpen(true)}
            className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <Plus className="h-4 w-4" />
          </button>
        </div>
      )}
      <nav className="flex flex-col gap-1 overflow-y-auto px-2" aria-label="Projects">
        {named.map(renderProject)}

        {loose.length > 0 && (
          <>
            <button
              type="button"
              onClick={() => setLooseOpen((o) => !o)}
              className={cn(rowClass, "text-muted-foreground", collapsed && "justify-center px-0")}
              aria-expanded={looseOpen}
            >
              <ChevronRight className={cn("h-4 w-4 shrink-0 transition-transform", looseOpen && "rotate-90")} />
              {!collapsed && <span className="truncate">Loose ({loose.length})</span>}
            </button>
            {looseOpen && loose.map(renderProject)}
          </>
        )}
      </nav>
      <AddProjectDialog open={addOpen} onOpenChange={setAddOpen} />
    </div>
  );
}
