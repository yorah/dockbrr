import { Badge } from "./ui/badge";
import type { Service } from "@/api/types";

export type Status =
  | "up-to-date"
  | "update-available"
  | "pinned"
  | "error"
  | "updating"
  | "stopped"
  | "restarting"
  | "gone"
  | "dismissed"
  | "rolled-back"
  | "drifted";

// Container states the Docker layer records for a non-running-but-present
// workload. "gone" (discovery no longer sees the container at all) is
// deliberately excluded: it gets its own Status so removed services can be
// hidden/shown independently of the "Stopped" stats tile and filter.
export const STOPPED_STATES = new Set(["exited", "dead"]);

export function isStopped(state: string): boolean {
  return STOPPED_STATES.has(state);
}

const LABEL: Record<Status, string> = {
  "up-to-date": "Up to date",
  "update-available": "Update available",
  pinned: "Pinned",
  error: "Error",
  updating: "Updating",
  stopped: "Stopped",
  restarting: "Restarting",
  gone: "Gone",
  dismissed: "Dismissed",
  "rolled-back": "Rolled back",
  drifted: "Drifted",
};

const VARIANT: Record<Status, "default" | "success" | "warning" | "danger" | "info"> = {
  "up-to-date": "success",
  "update-available": "warning",
  pinned: "default",
  error: "danger",
  updating: "info",
  stopped: "danger",
  restarting: "warning",
  gone: "default",
  dismissed: "default",
  "rolled-back": "default",
  drifted: "warning",
};

export function computeStatus(
  svc: Service & { drifted?: boolean },
  update: { open: boolean; dismissed?: boolean; rolledBack?: boolean } | undefined,
  opts: { updating?: boolean } = {},
): Status {
  if (opts.updating) return "updating";
  if (svc.state === "gone") return "gone";
  if (isStopped(svc.state)) return "stopped";
  if (svc.state === "restarting") return "restarting";
  if (svc.drifted) return "drifted";
  if (svc.pinned) return "pinned";
  if (svc.state === "error") return "error";
  if (update?.open) return "update-available";
  if (update?.dismissed) return "dismissed";
  if (update?.rolledBack) return "rolled-back";
  return "up-to-date";
}

export function StatusBadge({ status }: { status: Status }) {
  return <Badge variant={VARIANT[status]}>{LABEL[status]}</Badge>;
}
