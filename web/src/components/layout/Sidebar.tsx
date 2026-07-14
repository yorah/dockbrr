import { LogOut } from "lucide-react";
import { cn } from "@/lib/cn";
import { Separator } from "@/components/ui/separator";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { SidebarNav, rowClass } from "@/components/layout/SidebarNav";
import { SidebarProjects } from "@/components/layout/SidebarProjects";
import { useStatus } from "@/hooks/queries";
import { useLogout } from "@/hooks/mutations";

export function Sidebar({ collapsed, className }: { collapsed: boolean; className?: string }) {
  const logout = useLogout();
  const status = useStatus();

  const logoutButton = (
    <button
      type="button"
      onClick={() => logout.mutate()}
      disabled={logout.isPending}
      className={cn(rowClass, "disabled:opacity-50", collapsed && "justify-center px-0")}
    >
      <LogOut className="h-4 w-4 shrink-0" />
      {!collapsed && <span>Logout</span>}
      {collapsed && <span className="sr-only">Logout</span>}
    </button>
  );

  return (
    <aside
      className={cn(
        "flex shrink-0 flex-col gap-2 border-r border-border bg-card py-3 transition-[width] duration-200",
        collapsed ? "w-14" : "w-60",
        className,
      )}
    >
      <div className={cn("flex items-center gap-2 px-4 pb-1", collapsed && "justify-center px-0")}>
        <img src="/favicon.svg" alt="" className="h-5 w-5 shrink-0" />
        {!collapsed && <span className="text-base font-semibold">dockbrr</span>}
      </div>

      <SidebarNav collapsed={collapsed} />

      <Separator className="my-1" />

      <SidebarProjects collapsed={collapsed} />

      <div className="mt-auto flex flex-col gap-2">
        <Separator />
        <div className="px-2">
          {collapsed ? (
            <Tooltip>
              <TooltipTrigger asChild>{logoutButton}</TooltipTrigger>
              <TooltipContent side="right">Logout</TooltipContent>
            </Tooltip>
          ) : (
            logoutButton
          )}
        </div>
        <Separator />
        {status.data && (
          <span className={cn("px-4 text-xs text-muted-foreground", collapsed && "px-0 text-center text-[10px]")}>
            v{status.data.version}
          </span>
        )}
      </div>
    </aside>
  );
}
