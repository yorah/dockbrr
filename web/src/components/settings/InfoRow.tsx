import type { ReactNode } from "react";

export interface InfoRowProps {
  label: string;
  value: ReactNode;
  /** Muted second line under the value (e.g. "13d 14h ago"). */
  sub?: ReactNode;
}

// A read-only key/value line. Callers wrap a run of rows in
// <dl className="divide-y divide-border"> so the dividers land between rows.
export function InfoRow({ label, value, sub }: InfoRowProps) {
  return (
    <div className="grid grid-cols-[10rem_1fr] gap-4 py-3 max-sm:grid-cols-1 max-sm:gap-1">
      <dt className="text-xs font-medium tracking-wider text-muted-foreground uppercase">{label}</dt>
      <dd className="space-y-0.5">
        <div className="font-mono text-sm break-all">{value}</div>
        {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
      </dd>
    </div>
  );
}
