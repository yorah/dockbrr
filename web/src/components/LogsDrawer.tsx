import { useEffect, useState, useCallback } from "react";
import { RefreshCw } from "lucide-react";
import {
  Drawer,
  DrawerContent,
  DrawerHeader,
  DrawerTitle,
  DrawerDescription,
} from "@/components/ui/drawer";
import { Button } from "@/components/ui/button";
import { fetchServiceLogs } from "@/hooks/queries";
import type { Service } from "@/api/types";

export interface LogsDrawerProps {
  service: Service | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// Bounded log tail, fetched on demand rather than streamed (no follow/live
// tail): opens with a fresh fetch, and a Refresh button re-fetches the same
// bounded tail on request. Mirrors ChangelogDrawer's drawer shell.
export function LogsDrawer({ service, open, onOpenChange }: LogsDrawerProps) {
  const [logs, setLogs] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [isError, setIsError] = useState(false);

  const load = useCallback(() => {
    if (!service) return;
    setIsLoading(true);
    setIsError(false);
    fetchServiceLogs(service.id)
      .then((res) => {
        setLogs(res.logs);
      })
      .catch(() => {
        setIsError(true);
      })
      .finally(() => {
        setIsLoading(false);
      });
  }, [service]);

  useEffect(() => {
    if (open && service) {
      setLogs(null);
      load();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, service?.id]);

  if (!service) return null;

  return (
    <Drawer open={open} onOpenChange={onOpenChange}>
      <DrawerContent className="w-full max-w-md gap-4 overflow-y-auto sm:max-w-lg">
        <DrawerHeader>
          <DrawerTitle>{service.name}</DrawerTitle>
          <DrawerDescription>Recent log tail</DrawerDescription>
        </DrawerHeader>

        <div>
          <Button variant="outline" size="sm" onClick={load} disabled={isLoading}>
            <RefreshCw className={isLoading ? "mr-2 h-4 w-4 animate-spin" : "mr-2 h-4 w-4"} />
            Refresh
          </Button>
        </div>

        {isLoading && !logs && <p className="text-sm text-muted-foreground">Loading…</p>}
        {isError && <p className="text-sm text-danger">Failed to load logs.</p>}
        {!isError && logs !== null && (
          <div className="overflow-x-auto rounded-md border border-border bg-muted">
            <pre className="max-h-[60vh] overflow-auto p-3 font-mono text-xs">{logs}</pre>
          </div>
        )}
      </DrawerContent>
    </Drawer>
  );
}
