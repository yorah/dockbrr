import {
  Drawer,
  DrawerContent,
  DrawerHeader,
  DrawerTitle,
  DrawerDescription,
} from "@/components/ui/drawer";
import { Changelog } from "@/components/Changelog";
import { DigestShort } from "@/components/DigestShort";
import type { Service, Update } from "@/api/types";

export interface ChangelogDrawerProps {
  update: Update;
  service: Service;
  onClose: () => void;
}

// The dashboard's Changelog column can hand this drawer a pending (available),
// dismissed, or applied update. joinRows puts dismissed updates in r.update
// too, so the header label must reflect the actual status rather than
// assuming "not applied" means "pending".
function statusLabel(status: Update["status"]): string {
  switch (status) {
    case "applied":
      return "Last applied update";
    case "dismissed":
      return "Dismissed update";
    default:
      return "Pending update";
  }
}

// Read-only companion to ReviewDrawer: shows an update's cached changelog with
// no Apply/Dismiss controls. The dashboard opens it for the update behind the
// Changelog column, which, once the update has been applied, is the service's
// last applied update rather than a pending one.
export function ChangelogDrawer({ update, service, onClose }: ChangelogDrawerProps) {
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
          <DrawerDescription>
            {statusLabel(update.status)} · {update.tag}
          </DrawerDescription>
        </DrawerHeader>

        <section className="flex flex-col gap-1 text-sm">
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
      </DrawerContent>
    </Drawer>
  );
}
