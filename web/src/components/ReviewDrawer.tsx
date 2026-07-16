import { toast } from "sonner";
import {
  Drawer,
  DrawerContent,
  DrawerHeader,
  DrawerTitle,
  DrawerDescription,
  DrawerFooter,
} from "@/components/ui/drawer";
import { Button } from "@/components/ui/button";
import { SeverityDelta } from "@/components/SeverityDelta";
import { DigestShort } from "@/components/DigestShort";
import { Changelog } from "@/components/Changelog";
import { CommandPreview } from "@/components/CommandPreview";
import { useApply, useDismiss, useRestore } from "@/hooks/mutations";
import type { Project, Service, Update, Scope } from "@/api/types";

export interface ReviewDrawerProps {
  update: Update;
  service: Service;
  project: Project;
  onClose: () => void;
  /** Called with the new job id after a successful Apply (Task 13 opens the live job/log panel). */
  onApplied: (jobId: number) => void;
}

// The drawer reviews one service's update, so it only ever applies that service.
// Project-wide applies live on the project row's Apply-all button.
const SCOPE: Scope = "service";

export function ReviewDrawer({ update, service, project, onClose, onApplied }: ReviewDrawerProps) {
  const apply = useApply();
  const dismiss = useDismiss();
  const restore = useRestore();
  // A gone service has no container to recreate. Applying would just create
  // a fresh one for something that was removed. The backend rejects this too;
  // disabling here just avoids a round trip to find that out.
  const isGone = service.state === "gone";

  function handleApply() {
    apply.mutate(
      { id: update.id, scope: SCOPE },
      {
        onSuccess: (res) => onApplied(res.job_id),
        onError: () => toast.error("Failed to start apply. Please try again."),
      },
    );
  }

  function handleDismiss() {
    dismiss.mutate(update.id, {
      onSuccess: () => onClose(),
      onError: () => toast.error("Failed to dismiss update. Please try again."),
    });
  }

  function handleRestore() {
    restore.mutate(update.id, {
      onSuccess: () => onClose(),
      onError: () => toast.error("Failed to restore update. Please try again."),
    });
  }

  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DrawerContent className="w-full max-w-md gap-4 overflow-y-auto sm:max-w-lg">
        <DrawerHeader>
          <DrawerTitle>{service.name}</DrawerTitle>
          <DrawerDescription>{project.name}</DrawerDescription>
        </DrawerHeader>

        {isGone && (
          <div
            role="alert"
            className="rounded-md border border-danger/40 bg-danger/15 px-3 py-2 text-xs text-danger"
          >
            This service's container is gone. It can't be applied until it's seen running again.
          </div>
        )}
        {service.pinned && (
          <div
            role="alert"
            className="rounded-md border border-warning/40 bg-warning/15 px-3 py-2 text-xs text-warning"
          >
            This service is pinned. Applying this update will override the pin.
          </div>
        )}

        <section className="flex items-start justify-between gap-4">
          <div className="flex flex-col gap-1 text-sm">
            <span className="font-medium">{update.tag}</span>
            {update.from_version && update.to_version && update.from_version !== update.to_version && (
              <span className="text-xs">
                <span>{update.from_version}</span>
                <span aria-hidden="true"> → </span>
                <span className="font-medium">{update.to_version}</span>
              </span>
            )}
            <span className="flex items-center gap-1 text-xs opacity-70">
              <DigestShort digest={update.from_digest} />
              <span aria-hidden="true">→</span>
              <DigestShort digest={update.to_digest} />
            </span>
          </div>
          <SeverityDelta severity={update.severity} />
        </section>

        <section>
          <h3 className="mb-1 text-sm font-medium">Changelog</h3>
          <Changelog markdown={update.changelog_text} status={update.changelog_url ? undefined : update.changelog_status} />
          {update.changelog_url && (
            <a
              href={update.changelog_url}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-1 inline-block text-xs text-primary hover:underline"
            >
              View full changelog ↗
            </a>
          )}
        </section>

        <section>
          <h3 className="mb-1 text-sm font-medium">Affected containers</h3>
          <ul className="text-sm">
            <li>
              {service.name} <span className="opacity-60">({service.image_ref})</span>
            </li>
          </ul>
        </section>

        <section>
          <h3 className="mb-2 text-sm font-medium">Command preview</h3>
          <CommandPreview updateId={update.id} scope={SCOPE} />
        </section>

        <DrawerFooter className="flex-row justify-end gap-2">
          {update.status === "dismissed" || update.status === "rolled_back" ? (
            <Button variant="outline" onClick={handleRestore} disabled={restore.isPending}>
              Restore
            </Button>
          ) : (
            <Button variant="outline" onClick={handleDismiss} disabled={dismiss.isPending}>
              Dismiss
            </Button>
          )}
          <Button onClick={handleApply} disabled={apply.isPending || isGone}>
            Apply
          </Button>
        </DrawerFooter>
      </DrawerContent>
    </Drawer>
  );
}
