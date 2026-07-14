import { HelpCircle } from "lucide-react";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

// HelpTooltip renders a small "?" icon that reveals an explanatory tooltip on
// hover/focus. The trigger is a real button so it is keyboard-reachable. It
// carries its own TooltipProvider so it works standalone (settings screens are
// rendered without the AppLayout root provider in unit tests); nesting under the
// root provider is harmless.
export function HelpTooltip({ text }: { text: string }) {
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label={text}
            className="text-muted-foreground hover:text-foreground focus-visible:text-foreground"
          >
            <HelpCircle className="h-4 w-4" />
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">{text}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
