// Confirmation shown before applying an update to dockbrr itself. The self-update
// swaps the running container via a detached helper, so the browser connection
// drops and reconnects on the new version. Reused by every apply trigger.
export const SELF_UPDATE_CONFIRM =
  "Update dockbrr itself? dockbrr will pull the new image and hand the container swap to a short-lived helper, then restart. This page will briefly disconnect and reconnect on the new version. Continue?";

// selfUpdateErrorMessage maps a check error_kind to user-facing copy. rate_limited
// gets the "add a token" hint; anything else is a generic unreachable message.
export function selfUpdateErrorMessage(kind: string | undefined): string | null {
  if (kind === "rate_limited") {
    return "GitHub rate limit reached. Add a GitHub token in Settings to raise the limit.";
  }
  if (kind === "unreachable") {
    return "Couldn't reach GitHub to check for updates. Try again shortly.";
  }
  return null;
}
