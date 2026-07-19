import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { useProjectCompose } from "@/hooks/queries";

export interface ComposeModalProps {
  projectId: number;
  projectName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

const PATH_MAPPING_DOC = "https://github.com/yorah/dockbrr/blob/main/docs/path-mapping.md";

// Shown when a compose file can't be read: in a containerized dockbrr that
// almost always means the project directory isn't bind-mounted at its host
// path. Uses the failing file's directory to show a copy-pastable mount.
function MountHint({ paths }: { paths: string[] }) {
  const dirs = [...new Set(paths.map((p) => p.slice(0, p.lastIndexOf("/")) || "/"))];
  return (
    <div className="rounded-md border border-border bg-muted p-3 text-sm text-muted-foreground">
      <p>
        dockbrr can see this project but not its files. If dockbrr runs in a
        container, mount the project directory at the same path it has on the
        host, then restart dockbrr:
      </p>
      <pre className="mt-2 overflow-x-auto rounded bg-background p-2 font-mono text-xs">
        {`# docker run: add\n${dirs.map((d) => `-v ${d}:${d}`).join("\n")}\n\n# compose: add under dockbrr's volumes:\n${dirs.map((d) => `- ${d}:${d}`).join("\n")}`}
      </pre>
      <p className="mt-2">
        Standalone containers need no mounts; only compose projects do. Details:{" "}
        <a
          href={PATH_MAPPING_DOC}
          target="_blank"
          rel="noreferrer"
          className="underline hover:text-foreground"
        >
          path mapping guide
        </a>
        .
      </p>
    </div>
  );
}

export function ComposeModal({ projectId, projectName, open, onOpenChange }: ComposeModalProps) {
  const { data, isLoading, isError } = useProjectCompose(projectId, open);
  const unreadable = (data?.files ?? []).filter((f) => f.error).map((f) => f.path);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{projectName}</DialogTitle>
          <DialogDescription>Compose file(s) (read-only)</DialogDescription>
        </DialogHeader>

        {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
        {isError && (
          <p className="text-sm text-danger">Failed to load compose files.</p>
        )}

        {data?.files.map((file) => (
          <section key={file.path} className="flex flex-col gap-1">
            <h3 className="text-sm font-medium">{file.path}</h3>
            {file.error ? (
              <p className="text-sm text-danger">{file.error}</p>
            ) : (
              <div className="overflow-x-auto rounded-md border border-border bg-muted">
                <pre className="max-h-80 overflow-y-auto p-3 font-mono text-xs">{file.content}</pre>
              </div>
            )}
          </section>
        ))}

        {unreadable.length > 0 && <MountHint paths={unreadable} />}
      </DialogContent>
    </Dialog>
  );
}
