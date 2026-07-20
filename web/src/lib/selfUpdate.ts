// Confirmation shown before applying an update to dockbrr itself. The self-update
// swaps the running container via a detached helper, so the browser connection
// drops and reconnects on the new version. Reused by every apply trigger.
export const SELF_UPDATE_CONFIRM =
  "Update dockbrr itself? dockbrr will pull the new image and hand the container swap to a short-lived helper, then restart. This page will briefly disconnect and reconnect on the new version. Continue?";
