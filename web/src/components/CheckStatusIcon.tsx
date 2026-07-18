import { AlertTriangle, CircleSlash } from "lucide-react";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

const STATUS_TEXT: Record<string, string> = {
  rate_limited:
    "Registry rate limit hit while checking this image. dockbrr retries on the next scan; registry credentials raise the limit.",
  error:
    "Registry lookup failed (network or registry error). dockbrr retries on the next scan.",
  not_found:
    "Image not found in its registry. A locally built image can't be checked for updates; if this is a private image, add credentials in Settings, Registries.",
};

// CheckStatusIcon renders the per-service registry check outcome in the
// "Last checked" column: nothing when the check was ok, otherwise a small icon
// whose tooltip explains what happened and what (if anything) to do about it.
// Carries its own TooltipProvider so it works standalone in tests; nesting
// under a root provider is harmless.
export function CheckStatusIcon({ status }: { status: string }) {
  const text = STATUS_TEXT[status];
  if (!text) return null;
  let icon;
  switch (status) {
    case "rate_limited":
      icon = <AlertTriangle aria-label="Registry rate-limited" className="h-3.5 w-3.5 text-warning" />;
      break;
    case "error":
      icon = <AlertTriangle aria-label="Registry error" className="h-3.5 w-3.5 text-danger" />;
      break;
    default:
      icon = <CircleSlash aria-label="Image not in registry" className="h-3.5 w-3.5 text-muted-foreground" />;
  }
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span tabIndex={0} className="inline-flex cursor-help">{icon}</span>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">{text}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
