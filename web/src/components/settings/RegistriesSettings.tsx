import { useState } from "react";
import { toast } from "sonner";
import { useRegistries, useSettings } from "@/hooks/queries";
import { useAddRegistry, useDeleteRegistry } from "@/hooks/mutations";
import { useSettingsForm, type SettingKey } from "@/hooks/useSettingsForm";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { SettingsCard } from "@/components/settings/SettingsCard";

// The GitHub token is write-only: the settings DTO carries only the boolean
// `github_token_set`, never the value. So the token is not a tracked form field,
// it rides along as the `extra` patch of form.save(), which means `github_token`
// reaches the PUT body only when the user actually typed something.
const EDITABLE_KEYS: SettingKey[] = [];

export function RegistriesSettings() {
  const { data: registries } = useRegistries();
  const addRegistry = useAddRegistry();
  const deleteRegistry = useDeleteRegistry();
  const [host, setHost] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const settings = useSettings();
  const form = useSettingsForm(EDITABLE_KEYS);
  const [githubToken, setGithubToken] = useState("");

  return (
    <div className="space-y-4">
      <SettingsCard
        title="Registry credentials"
        description="Credentials dockbrr uses to query private registries."
      >
        {/* Cards span the full settings column like every other page; only the
            inner content is width-capped (same pattern as Logs/Auto-update). */}
        <div className="max-w-2xl space-y-6">
          <div className="flex items-center gap-1.5">
            <p className="text-sm text-muted-foreground">
              Credentials for private registries.
            </p>
            <HelpTooltip text="dockbrr reads public images (Docker Hub, public ghcr.io/quay.io) anonymously, no entry needed. Add a registry only for private images: dockbrr retries with these credentials when a registry returns 401 Unauthorized. Matched by exact host." />
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Host</TableHead>
                <TableHead>Username</TableHead>
                <TableHead>Created</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {(registries ?? []).map((r) => (
                <TableRow key={r.RegistryHost}>
                  <TableCell>{r.RegistryHost}</TableCell>
                  <TableCell>{r.Username}</TableCell>
                  <TableCell>{r.CreatedAt}</TableCell>
                  <TableCell>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={deleteRegistry.isPending}
                      onClick={() =>
                        deleteRegistry.mutate(r.RegistryHost, {
                          onSuccess: () => toast.success("Registry removed"),
                        })
                      }
                    >
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>

          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (!host || !username || !password) return;
              addRegistry.mutate(
                { host, username, password },
                {
                  onSuccess: () => {
                    toast.success("Registry added");
                    setHost("");
                    setUsername("");
                    setPassword("");
                  },
                },
              );
            }}
          >
            <h3 className="text-sm font-semibold">Add registry</h3>
            <div className="space-y-1">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="registry-host">Host</Label>
                <HelpTooltip text="Registry hostname exactly as it appears in the image ref, e.g. ghcr.io, registry.gitlab.com, or a self-hosted registry.example.com. Not a full image path." />
              </div>
              <Input id="registry-host" value={host} onChange={(e) => setHost(e.target.value)} placeholder="ghcr.io" />
            </div>
            <div className="space-y-1">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="registry-username">Username</Label>
                <HelpTooltip text="Registry account username. For ghcr.io this is your GitHub username; some registries accept any non-empty value alongside a token." />
              </div>
              <Input id="registry-username" value={username} onChange={(e) => setUsername(e.target.value)} />
            </div>
            <div className="space-y-1">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="registry-password">Password</Label>
                <HelpTooltip text="Password or access token (e.g. a GitHub PAT with read:packages for ghcr.io). Stored encrypted (AES-256-GCM) and write-only, never displayed after saving." />
              </div>
              <Input
                id="registry-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <Button type="submit" disabled={addRegistry.isPending}>Add registry</Button>
          </form>
        </div>
      </SettingsCard>

      <SettingsCard title="GitHub token" description="Used to fetch changelogs and release notes at a higher rate limit.">
        <div className="max-w-lg space-y-1">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="github_token">GitHub token</Label>
            <HelpTooltip text="Lifts the GitHub changelog rate limit from 60 to 5000 requests/hour (unauthenticated requests are throttled to 60/hour, which hides changelogs). Create one at github.com/settings/tokens as a classic token with no scopes checked (public release notes need none), then paste it here. Write-only, never displayed." />
          </div>
          <Input
            id="github_token"
            type="password"
            autoComplete="off"
            placeholder={settings.data === undefined ? "" : settings.data.github_token_set ? "Set" : "Not set"}
            value={githubToken}
            onChange={(e) => setGithubToken(e.target.value)}
          />
          <div className="pt-2">
            <Button
              onClick={() => form.save({ github_token: githubToken.trim() }, () => setGithubToken(""))}
              disabled={form.isSaving || githubToken.trim().length === 0}
            >
              Save token
            </Button>
          </div>
        </div>
      </SettingsCard>
    </div>
  );
}
