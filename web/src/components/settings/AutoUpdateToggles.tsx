import { useProjects } from "@/hooks/queries";
import { useToggleProjectAuto, useToggleServiceAuto } from "@/hooks/mutations";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { SettingsCard } from "@/components/settings/SettingsCard";

function serviceModeValue(enabled: boolean | null): string {
  if (enabled === null) return "inherit";
  return enabled ? "on" : "off";
}

export function AutoUpdateToggles() {
  const { data: projects } = useProjects();
  const toggleProject = useToggleProjectAuto();
  const toggleService = useToggleServiceAuto();

  return (
    <SettingsCard title="Auto-update" description="Apply updates automatically, per project or per service.">
      <div className="max-w-2xl space-y-6">
        <div className="flex items-center gap-1.5">
          <p className="text-sm text-muted-foreground">
            Automatically apply available updates on the next poll.
          </p>
          <HelpTooltip text="When on, dockbrr applies available updates for the project (or service) without manual review, on each poll interval (set in General settings). Genuinely pinned services are never auto-updated." />
        </div>
        {(projects ?? []).map((project) => (
          <div key={project.id} className="rounded-lg border border-border p-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-1.5">
                <Label htmlFor={`project-auto-${project.id}`} className="font-medium">
                  {project.name}
                </Label>
                <HelpTooltip text="Auto-update every service in this project, unless a service overrides it below." />
              </div>
              <Switch
                id={`project-auto-${project.id}`}
                checked={project.auto_update_enabled}
                onCheckedChange={(checked) => toggleProject.mutate({ id: project.id, enabled: checked })}
              />
            </div>

            {project.services.length > 0 && (
              <div className="mt-3 space-y-2 border-t border-border pt-3">
                {project.services.map((service) => (
                  <div key={service.id} className="flex items-center justify-between text-sm">
                    <span>{service.name}</span>
                    <Select
                      value={serviceModeValue(service.auto_update_enabled)}
                      onValueChange={(v) =>
                        toggleService.mutate({
                          id: service.id,
                          enabled: v === "inherit" ? null : v === "on",
                        })
                      }
                    >
                      <SelectTrigger aria-label={`${service.name} auto-update`} className="w-40">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="on">On</SelectItem>
                        <SelectItem value="off">Off</SelectItem>
                        <SelectItem value="inherit">Inherit (project)</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </SettingsCard>
  );
}
