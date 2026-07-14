import { useUpdatePreview } from "@/hooks/queries";
import type { Scope } from "@/api/types";

export interface CommandPreviewProps {
  updateId: number;
  scope: Scope;
}

// The exact `docker compose pull`/`up` command dockbrr will run. Mutations never
// shell out to Docker outside the job engine, so this is a preview, not a live
// invocation. Rendered as plain text (mono block), copyable by selection.
export function CommandPreview({ updateId, scope }: CommandPreviewProps) {
  const { data, isLoading, isError } = useUpdatePreview(updateId, scope);

  if (isLoading) {
    return (
      <div className="space-y-2" aria-label="Loading command preview">
        <div className="h-4 w-5/6 animate-pulse rounded bg-muted" />
        <div className="h-4 w-2/3 animate-pulse rounded bg-muted" />
      </div>
    );
  }

  if (isError || !data) {
    return <p className="text-sm text-danger">Failed to load command preview.</p>;
  }

  return (
    <pre className="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">
      <code>{data.pull}</code>
      {"\n"}
      <code>{data.up}</code>
    </pre>
  );
}
