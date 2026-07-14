import { Outlet } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { Sidebar } from "@/components/layout/Sidebar";
import { Topbar } from "@/components/layout/Topbar";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useSidebar } from "@/hooks/useSidebar";
import { useEventStream } from "@/hooks/useEventStream";

export function AppLayout() {
  const { collapsed, toggle, isNarrow } = useSidebar();
  // Global SSE refresh stream: maps push events to query invalidations. Mounted
  // here since AppLayout renders only when authenticated (the cookie exists).
  useEventStream();
  // On narrow viewports the user can still expand the sidebar; when they do,
  // it overlays the content instead of squeezing it into a sliver.
  const overlay = !collapsed && isNarrow;
  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex min-h-screen bg-background text-foreground">
        {overlay && (
          <div className="fixed inset-0 z-30 bg-overlay/50" aria-hidden="true" onClick={toggle} />
        )}
        <Sidebar collapsed={collapsed} className={overlay ? "fixed inset-y-0 left-0 z-40" : undefined} />
        <div className="flex min-w-0 flex-1 flex-col">
          <Topbar collapsed={collapsed} onToggle={toggle} />
          <main className="flex min-h-0 w-full flex-1 flex-col p-4">
            <Outlet />
          </main>
        </div>
        <Toaster />
      </div>
    </TooltipProvider>
  );
}
