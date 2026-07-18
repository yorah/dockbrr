import { useQuery, useQueryClient } from "@tanstack/react-query";
import { notify } from "@/lib/notify";
import { apiFetch } from "@/api/client";
import { keys } from "@/api/keys";
import { useSaveSettings } from "@/hooks/mutations";
import { Label } from "@/components/ui/label";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { SettingsCard } from "@/components/settings/SettingsCard";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { LogConfig, LogFile } from "@/api/types";

const LEVELS = ["trace", "debug", "info", "warn", "error"];

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

export function LogsSettings() {
  const config = useQuery({
    queryKey: keys.logConfig,
    queryFn: () => apiFetch<LogConfig>("/api/logs/config"),
  });
  const files = useQuery({
    queryKey: keys.logFiles,
    queryFn: () => apiFetch<LogFile[]>("/api/logs/files"),
  });
  const save = useSaveSettings();
  const qc = useQueryClient();

  if (!config.data) {
    return <div className="h-40 animate-pulse rounded-lg bg-muted" role="status" aria-label="Loading logs settings" />;
  }

  const rows = files.data ?? [];

  return (
    <SettingsCard title="Logs" description="Log level, rotation, and downloadable log files.">
      <div className="max-w-2xl space-y-4">
        <div className="flex items-center gap-1.5">
          <p className="text-sm text-muted-foreground">
            Application logs. Level applies immediately; path, size and backups are set at startup.
          </p>
          <HelpTooltip text="dockbrr writes to the console and a rotating log file. Configure the file path and rotation via the DOCKBRR_LOG_* env vars or --log-* flags." />
        </div>

        <div className="grid grid-cols-2 gap-3 text-sm">
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="log_level">Level</Label>
              <HelpTooltip text="Minimum severity written to the log. Applied immediately, no restart." />
            </div>
            <select
              id="log_level"
              aria-label="Log level"
              className="w-full rounded-md border border-input bg-transparent px-2 py-1.5"
              value={config.data.level}
              onChange={(e) => {
                const level = e.target.value;
                // The select is controlled by this query's data (so an imported
                // level actually shows up), so move it optimistically instead of
                // leaving it on the old level until the PUT resolves. useSaveSettings
                // invalidates logConfig on settle, including on failure, which
                // snaps the select back to the level that is actually persisted.
                qc.setQueryData<LogConfig>(keys.logConfig, (o) => (o ? { ...o, level } : o));
                save.mutate(
                  { log_level: level },
                  { onSuccess: () => notify.success(`Log level: ${level}`) },
                );
              }}
            >
              {LEVELS.map((l) => (
                <option key={l} value={l}>
                  {l}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1">
            <Label>Path</Label>
            <p className="truncate text-muted-foreground" title={config.data.path}>
              {config.data.path}
            </p>
          </div>
          <div className="space-y-1">
            <Label>Max size</Label>
            <p className="text-muted-foreground">{config.data.maxSizeMB} MB</p>
          </div>
          <div className="space-y-1">
            <Label>Max backups</Label>
            <p className="text-muted-foreground">{config.data.maxBackups}</p>
          </div>
        </div>

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Last modified</TableHead>
              <TableHead>Size</TableHead>
              <TableHead className="text-right">Download</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  No log files yet.
                </TableCell>
              </TableRow>
            ) : (
              rows.map((f) => (
                <TableRow key={f.name}>
                  <TableCell className="font-mono text-xs">{f.name}</TableCell>
                  <TableCell>{new Date(f.modified).toLocaleString()}</TableCell>
                  <TableCell>{humanSize(f.size)}</TableCell>
                  <TableCell className="text-right">
                    <a
                      href={`/api/logs/files/${encodeURIComponent(f.name)}/download`}
                      className="text-primary hover:underline"
                      aria-label={`Download ${f.name}`}
                      download
                    >
                      ⬇
                    </a>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </SettingsCard>
  );
}
