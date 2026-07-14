import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, DefaultHint } from "@/components/settings/SettingsCard";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { useSettingsForm, type SettingKey } from "@/hooks/useSettingsForm";

const KEYS: SettingKey[] = ["poll_interval_seconds", "scan_on_start", "concurrency", "cache_ttl_seconds"];

const NUMBER_FIELDS: Array<{ key: SettingKey; label: string; help: string }> = [
  {
    key: "poll_interval_seconds",
    label: "Poll interval (seconds)",
    help: "How often dockbrr scans your stacks for available image updates. Auto-update (when enabled) also applies updates on this interval.",
  },
  {
    key: "concurrency",
    label: "Concurrency",
    help: "Maximum number of registry checks run at once. Takes effect after a restart.",
  },
  {
    key: "cache_ttl_seconds",
    label: "Registry cache TTL (seconds)",
    help: "How long a registry digest lookup is cached before dockbrr re-queries the registry.",
  },
];

export function ScanningSettings() {
  const { data, form, dirty, isSaving, setField, isDefault, save } = useSettingsForm(KEYS);
  if (!data) {
    return <div className="h-40 animate-pulse rounded-lg bg-muted" role="status" aria-label="Loading scanning settings" />;
  }

  return (
    <SettingsCard title="Scanning" description="How often dockbrr looks for new images, and how hard it hits registries.">
      <div className="max-w-lg space-y-4">
        {NUMBER_FIELDS.map(({ key, label, help }) => (
          <div key={key} className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor={key}>{label}</Label>
              <HelpTooltip text={help} />
              {isDefault(key) && <DefaultHint />}
            </div>
            <Input
              id={key}
              type="number"
              className={isDefault(key) ? "text-muted-foreground" : undefined}
              value={form[key] ?? ""}
              onChange={(e) => setField(key, e.target.value)}
            />
          </div>
        ))}

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="scan_on_start">Scan on launch</Label>
            <HelpTooltip text="Run one scan as soon as dockbrr starts, instead of waiting a full poll interval for the first one. If a project has auto-update on, eligible updates found at boot are applied immediately too, same as any scheduled scan." />
            {isDefault("scan_on_start") && <DefaultHint />}
          </div>
          <Switch
            id="scan_on_start"
            checked={form.scan_on_start === "true"}
            onCheckedChange={(checked) => setField("scan_on_start", checked ? "true" : "false")}
          />
        </div>

        <div className="flex items-center gap-3">
          <Button onClick={() => save()} disabled={isSaving || !dirty}>Save</Button>
          {dirty && (
            <span role="status" className="text-sm text-warning">
              Unsaved changes
            </span>
          )}
        </div>
      </div>
    </SettingsCard>
  );
}
