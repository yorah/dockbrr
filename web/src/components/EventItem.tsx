import { useState } from "react";
import { CheckCircle2, Circle, Eye, EyeOff, Info, PlayCircle, RotateCcw, XCircle, type LucideIcon } from "lucide-react";
import { DigestShort } from "@/components/DigestShort";
import { RelativeTime } from "@/components/RelativeTime";
import { Changelog } from "@/components/Changelog";
import type { ServiceEvent } from "@/api/types";

export interface EventKindMeta {
  label: string;
  icon: LucideIcon;
  className: string;
}

// kind -> icon + color, per Task 14 brief: detected=info, apply_started=muted,
// succeeded=green, failed/apply_failed=red, rolled_back=amber, dismissed=slate.
const KIND_META: Record<string, EventKindMeta> = {
  detected: { label: "Update detected", icon: Info, className: "text-info" },
  apply_started: { label: "Apply started", icon: PlayCircle, className: "text-muted-foreground" },
  succeeded: { label: "Succeeded", icon: CheckCircle2, className: "text-success" },
  failed: { label: "Failed", icon: XCircle, className: "text-danger" },
  apply_failed: { label: "Apply failed", icon: XCircle, className: "text-danger" },
  rolled_back: { label: "Rolled back", icon: RotateCcw, className: "text-warning" },
  dismissed: { label: "Dismissed", icon: EyeOff, className: "text-muted-foreground" },
  restored: { label: "Restored", icon: Eye, className: "text-success" },
};

const DEFAULT_META: EventKindMeta = { label: "Event", icon: Circle, className: "text-muted-foreground" };

export function kindMeta(kind: string): EventKindMeta {
  return KIND_META[kind] ?? DEFAULT_META;
}

export interface EventItemProps {
  event: ServiceEvent;
  /** Called with the job id when the user wants to view that job's log. */
  onViewJob?: (jobId: number) => void;
  /** Optional changelog text to display in a disclosure. */
  changelog_text?: string | null;
  /** Optional changelog URL to link to. */
  changelog_url?: string | null;
}

export function EventItem({
  event,
  onViewJob,
  changelog_text,
  changelog_url,
}: EventItemProps) {
  const meta = kindMeta(event.kind);
  const Icon = meta.icon;
  const hasDigests = Boolean(event.from_digest || event.to_digest);
  const [changelogOpen, setChangelogOpen] = useState(false);

  return (
    <li className="relative flex gap-3 pb-6 last:pb-0">
      <div className="flex flex-col items-center">
        <Icon className={`h-5 w-5 shrink-0 ${meta.className}`} aria-hidden="true" />
        <div className="mt-1 w-px flex-1 bg-border" aria-hidden="true" />
      </div>
      <div className="flex-1 pb-1">
        <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
          <span className={meta.className}>{meta.label}</span>
          <RelativeTime iso={event.created_at} />
        </div>

        {hasDigests && (
          <div className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">
            <DigestShort digest={event.from_digest} />
            <span aria-hidden="true">&rarr;</span>
            <DigestShort digest={event.to_digest} />
          </div>
        )}

        {event.message && (
          <p className="mt-1 text-sm text-foreground">{event.message}</p>
        )}

        {event.ref_job_id != null && onViewJob && (
          <button
            type="button"
            className="mt-1 text-xs font-medium text-info underline-offset-2 hover:underline"
            onClick={() => onViewJob(event.ref_job_id as number)}
          >
            View job #{event.ref_job_id} log
          </button>
        )}

        {(changelog_text || changelog_url) && (
          <div className="mt-1">
            <button
              type="button"
              className="text-xs font-medium text-info underline-offset-2 hover:underline"
              onClick={() => setChangelogOpen((v) => !v)}
              aria-expanded={changelogOpen}
            >
              Changelog
            </button>
            {changelogOpen && (
              <div className="mt-1">
                <Changelog markdown={changelog_text ?? ""} />
                {changelog_url && (
                  <a
                    href={changelog_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="mt-1 inline-block text-xs text-primary hover:underline"
                  >
                    View full changelog ↗
                  </a>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </li>
  );
}
