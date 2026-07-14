import { PanelLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/ThemeToggle";

export function Topbar({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  return (
    <header className="flex items-center justify-between border-b border-border px-3 py-2">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        onClick={onToggle}
        aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
        aria-expanded={!collapsed}
      >
        <PanelLeft className="h-4 w-4" />
      </Button>
      <ThemeToggle />
    </header>
  );
}
