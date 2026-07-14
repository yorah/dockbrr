import { cn } from "@/lib/cn";

export const SEVERITY_COLOR: Record<string, string> = {
  major: "text-danger",
  minor: "text-warning",
  patch: "text-info",
  "digest-only": "text-muted-foreground",
};

export function SeverityDelta({ severity }: { severity: string }) {
  return (
    <span
      data-severity={severity}
      className={cn("text-xs font-medium", SEVERITY_COLOR[severity] ?? "text-muted-foreground")}
    >
      {severity}
    </span>
  );
}
