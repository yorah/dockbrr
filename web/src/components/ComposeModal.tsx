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

export function ComposeModal({ projectId, projectName, open, onOpenChange }: ComposeModalProps) {
  const { data, isLoading, isError } = useProjectCompose(projectId, open);

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
      </DialogContent>
    </Dialog>
  );
}
