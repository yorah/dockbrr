import { cn } from "@/lib/cn";

/** A 1px horizontal rule. `role="separator"` so tests and AT can find it. */
export function Separator({ className }: { className?: string }) {
  return <div role="separator" aria-orientation="horizontal" className={cn("h-px w-full bg-border", className)} />;
}
