import { useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { cn } from "@/lib/cn";
import { notify } from "@/lib/notify";
import { apiFetch } from "@/api/client";
import { keys } from "@/api/keys";
import { useSystemInfo, useSelfUpdate } from "@/hooks/queries";
import { useCheckForUpdates } from "@/hooks/mutations";
import { useNow } from "@/hooks/useNow";
import { Button } from "@/components/ui/button";
import { SettingsCard } from "@/components/settings/SettingsCard";
import { InfoRow } from "@/components/settings/InfoRow";

const DASH = "-";

// Uptime as "1d 2h 30m" / "2h 30m" / "45s", derived from started_at against a
// ticking clock, so it ages on screen without refetching.
function uptime(startedAt: string | undefined, now: Date): string {
  if (!startedAt) return DASH;
  const start = new Date(startedAt).getTime();
  if (Number.isNaN(start)) return DASH;
  let s = Math.max(0, Math.floor((now.getTime() - start) / 1000));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function formatDate(iso: string): string {
  if (!iso) return DASH;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? DASH : d.toLocaleString();
}

// checkedAgo renders "just now" / "5m ago" / "3h ago" / "2d ago" from an ISO
// timestamp against a ticking clock, so the last-checked line ages on screen.
function checkedAgo(iso: string | undefined, now: Date): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.floor((now.getTime() - t) / 1000));
  if (s < 60) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function Rows({ children }: { children: React.ReactNode }) {
  return <dl className="divide-y divide-border border-t border-border">{children}</dl>;
}

export function ApplicationSettings() {
  const { data, isLoading, refetch, isFetching } = useSystemInfo();
  const selfUpdate = useSelfUpdate();
  const check = useCheckForUpdates();
  const now = useNow(1_000);
  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);

  if (isLoading || !data) {
    return <div className="h-40 animate-pulse rounded-lg bg-muted" role="status" aria-label="Loading system info" />;
  }

  const commit = data.commit ? data.commit.slice(0, 7) : DASH;

  const su = selfUpdate.data;
  const rel = checkedAgo(su?.checked_at, now);
  // Only assert a verdict once a check has actually run (checked_at present);
  // otherwise show nothing rather than claiming "up to date" with no basis.
  const versionSub = su?.checked_at
    ? su.update_available
      ? `${su.latest} available${rel ? ` (checked ${rel})` : ""}`
      : `Up to date${rel ? ` (checked ${rel})` : ""}`
    : undefined;

  return (
    <div className="space-y-4">
      <SettingsCard
        title="Build"
        description="Version and build details."
        action={
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
              <RefreshCw className="mr-2 h-4 w-4" />
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => check.mutate()} disabled={check.isPending}>
              <RefreshCw className={cn("mr-2 h-4 w-4", check.isPending && "animate-spin")} />
              {check.isPending ? "Checking..." : "Check for updates"}
            </Button>
          </div>
        }
      >
        <Rows>
          <InfoRow label="Version" value={data.version} sub={versionSub} />
          <InfoRow label="Commit" value={commit} sub={data.commit_dirty ? "working tree was dirty at build time" : undefined} />
          <InfoRow label="Build date" value={formatDate(data.build_date)} />
        </Rows>
        {su?.error_kind === "rate_limited" && (
          <p className="mt-2 text-xs text-warning" role="status">
            GitHub rate limit reached.{" "}
            <a href="/settings/registries" className="text-primary hover:underline">
              Add a GitHub token
            </a>{" "}
            in Settings to raise the limit.
          </p>
        )}
        {su?.error_kind === "unreachable" && (
          <p className="mt-2 text-xs text-warning" role="status">
            Couldn't reach GitHub to check for updates. Try again shortly.
          </p>
        )}
      </SettingsCard>

      <SettingsCard title="Runtime" description="Server runtime information.">
        <Rows>
          <InfoRow label="Uptime" value={uptime(data.started_at, now)} />
          <InfoRow
            label="Runtime"
            value={
              <>
                <span>{data.go_version}</span> • <span>{data.platform}</span>
              </>
            }
          />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Docker" description="Daemon connection.">
        <Rows>
          <InfoRow label="Status" value={data.docker.reachable ? "Reachable" : "Unreachable"} />
          {data.docker.reachable && data.docker.version && (
            <InfoRow label="Daemon" value={data.docker.version} sub={data.docker.api_version ? `API ${data.docker.api_version}` : undefined} />
          )}
        </Rows>
      </SettingsCard>

      <SettingsCard title="Storage" description="Database and file system paths.">
        <Rows>
          <InfoRow label="Database" value={data.db_path} />
          <InfoRow label="Bind address" value={data.bind_addr} />
          <InfoRow label="Data directory" value={data.data_dir} />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Authentication" description="Authentication configuration.">
        <Rows>
          <InfoRow label="Current session" value={data.auth.username} sub={data.auth.method} />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Backup" description="Export or restore dockbrr's settings and registry credentials.">
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={() => {
              window.location.href = "/api/settings/export";
            }}
          >
            Export settings
          </Button>
          <Button variant="outline" onClick={() => fileRef.current?.click()}>
            Import settings
          </Button>
          <input
            ref={fileRef}
            type="file"
            accept="application/json"
            className="hidden"
            onChange={async (e) => {
              const file = e.target.files?.[0];
              if (!file) return;
              try {
                const body = JSON.parse(await file.text());
                await apiFetch("/api/settings/import", { method: "POST", body });
                notify.success("Settings imported");
                qc.invalidateQueries({ queryKey: keys.settings });
                qc.invalidateQueries({ queryKey: keys.registries });
                // An imported log_level is validated + applied server-side; the
                // Logs page reads this query independently of keys.settings.
                qc.invalidateQueries({ queryKey: keys.logConfig });
              } catch {
                notify.error("Import failed. Invalid file?");
              } finally {
                e.target.value = "";
              }
            }}
          />
        </div>
      </SettingsCard>
    </div>
  );
}
