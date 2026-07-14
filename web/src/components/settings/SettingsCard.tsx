import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface SettingsCardProps {
  title: string;
  description?: string;
  /** Right-aligned header slot (e.g. a Refresh or Save button). */
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}

export function SettingsCard({ title, description, action, className, children }: SettingsCardProps) {
  return (
    <section className={cn("rounded-lg border border-border bg-card", className)}>
      <div className="flex items-start justify-between gap-4 p-4">
        <div className="min-w-0 space-y-0.5">
          <h2 className="break-words text-sm font-semibold">{title}</h2>
          {description && <p className="break-words text-sm text-muted-foreground">{description}</p>}
        </div>
        {action && <div className="shrink-0">{action}</div>}
      </div>
      <div className="px-4 pb-4">{children}</div>
    </section>
  );
}

// Marks a field whose value still matches the server-side default, so an
// untouched setting reads differently from one deliberately set to that value.
export function DefaultHint() {
  return <span className="text-xs font-normal text-muted-foreground/70">default</span>;
}
