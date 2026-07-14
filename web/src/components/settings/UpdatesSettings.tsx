import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, DefaultHint } from "@/components/settings/SettingsCard";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { useSettingsForm, type SettingKey } from "@/hooks/useSettingsForm";

const KEYS: SettingKey[] = [
  "health_timeout_seconds",
  "health_poll_seconds",
  "write_back_compose",
  "auto_remove_gone",
  "default_auto_update_enabled",
  "gone_grace_seconds",
  "job_retention_days",
];

const NUMBER_FIELDS: Array<{ key: SettingKey; label: string; help: string }> = [
  {
    key: "health_timeout_seconds",
    label: "Health timeout (seconds)",
    help: "How long to wait for a recreated container to become healthy after an apply before rolling back.",
  },
  {
    key: "health_poll_seconds",
    label: "Health poll interval (seconds)",
    help: "How frequently the health check polls the recreated container during the timeout window.",
  },
  {
    key: "gone_grace_seconds",
    label: "Gone-removal grace (seconds)",
    help: "When auto-remove is on, how long a disappeared (gone) service is kept before it is deleted.",
  },
  {
    key: "job_retention_days",
    label: "Job history retention (days)",
    help: "Finished jobs (and their logs) older than this are deleted daily. Queued and running jobs are never removed. Set to 0 to keep job history forever.",
  },
];

export function UpdatesSettings() {
  const { data, form, dirty, isSaving, setField, isDefault, save } = useSettingsForm(KEYS);
  if (!data) {
    return <div className="h-40 animate-pulse rounded-lg bg-muted" role="status" aria-label="Loading updates settings" />;
  }

  return (
    <SettingsCard title="Updates" description="How updates are applied, health-gated, and cleaned up.">
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
            <Label htmlFor="write_back_compose">Write updates back to compose files</Label>
            <HelpTooltip text="On apply, rewrite the image tag in your compose file so the update survives a manual recreate." />
            {isDefault("write_back_compose") && <DefaultHint />}
          </div>
          <Switch
            id="write_back_compose"
            checked={form.write_back_compose === "true"}
            onCheckedChange={(checked) => setField("write_back_compose", checked ? "true" : "false")}
          />
        </div>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="auto_remove_gone">Auto-remove gone services &amp; empty projects</Label>
            <HelpTooltip text="Automatically delete services whose containers have disappeared past the grace period, plus any project left empty." />
            {isDefault("auto_remove_gone") && <DefaultHint />}
          </div>
          <Switch
            id="auto_remove_gone"
            checked={form.auto_remove_gone === "true"}
            onCheckedChange={(checked) => setField("auto_remove_gone", checked ? "true" : "false")}
          />
        </div>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="default_auto_update_enabled">Auto-update newly discovered projects</Label>
            <HelpTooltip text="New compose stacks found on this host start with auto-update off, unless you turn this on. Only affects stacks discovered from now on, existing projects are never touched." />
            {isDefault("default_auto_update_enabled") && <DefaultHint />}
          </div>
          <Switch
            id="default_auto_update_enabled"
            checked={form.default_auto_update_enabled === "true"}
            onCheckedChange={(checked) => setField("default_auto_update_enabled", checked ? "true" : "false")}
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
