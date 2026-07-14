import { useState } from "react";
import { toast } from "sonner";
import { useCreateProject } from "@/hooks/mutations";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { HelpTooltip } from "@/components/settings/HelpTooltip";

function parseConfigFiles(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export interface AddProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * Registers a compose project dockbrr could not auto-discover. Shared by the
 * sidebar "+" and the dashboard's Add project button, so both entry points get
 * the same form and the same invalidation on success (via useCreateProject).
 */
export function AddProjectDialog({ open, onOpenChange }: AddProjectDialogProps) {
  const createProject = useCreateProject();
  const [name, setName] = useState("");
  const [workingDir, setWorkingDir] = useState("");
  const [configFiles, setConfigFiles] = useState("");

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add project</DialogTitle>
          <DialogDescription>
            Register a compose project dockbrr did not discover from running containers.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!name || !workingDir) return;
            createProject.mutate(
              { name, working_dir: workingDir, config_files: parseConfigFiles(configFiles) },
              {
                onSuccess: (created) => {
                  toast.success(`Project "${created.name}" added`);
                  setName("");
                  setWorkingDir("");
                  setConfigFiles("");
                  onOpenChange(false);
                },
              },
            );
          }}
        >
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="project-name">Name</Label>
              <HelpTooltip text="Display name for this project in dockbrr. Any label you choose; it does not need to match the compose project name." />
            </div>
            <Input id="project-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="project-working-dir">Working directory</Label>
              <HelpTooltip text="Absolute path on the host where the compose files live. dockbrr runs all compose commands from here." />
            </div>
            <Input
              id="project-working-dir"
              value={workingDir}
              onChange={(e) => setWorkingDir(e.target.value)}
              placeholder="/srv/app"
            />
          </div>
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="project-config-files">Compose files</Label>
              <HelpTooltip text="Compose file names relative to the working directory. Separate multiple files with a comma or newline; order is preserved." />
            </div>
            <textarea
              id="project-config-files"
              className="flex min-h-16 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={configFiles}
              onChange={(e) => setConfigFiles(e.target.value)}
              placeholder="docker-compose.yml, docker-compose.override.yml"
            />
          </div>
          <Button type="submit" disabled={createProject.isPending}>Add project</Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}
