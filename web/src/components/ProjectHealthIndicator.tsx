import { cn } from "@/lib/cn";
import type { Dot } from "@/hooks/useProjectHealth";

const DOT_CLASS: Record<Dot, string> = {
  red: "bg-danger",
  amber: "bg-warning",
  green: "bg-success",
};
const DOT_LABEL: Record<Dot, string> = {
  red: "last job failed",
  amber: "updates available",
  green: "healthy",
};

/**
 * The per-project health glyph shared by the sidebar and the dashboard project
 * row: an update-count badge (only when there are open updates) followed by a
 * status dot (red = last job failed, amber = updates, green = healthy). Kept in
 * one place so both surfaces stay visually identical.
 */
export function ProjectHealthIndicator({
  updates,
  dot,
  className,
}: {
  updates: number;
  dot: Dot;
  className?: string;
}) {
  const label = updates > 0 ? `${updates} update${updates === 1 ? "" : "s"}, ${DOT_LABEL[dot]}` : DOT_LABEL[dot];
  return (
    <span className={cn("flex items-center gap-1.5", className)}>
      {updates > 0 && (
        <span className="rounded-full bg-warning/15 px-1.5 py-0.5 text-xs font-medium text-warning tabular-nums">
          {updates}
        </span>
      )}
      <span role="img" aria-label={label} className={cn("h-2 w-2 shrink-0 rounded-full", DOT_CLASS[dot])} />
    </span>
  );
}
