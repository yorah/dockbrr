import { useSyncExternalStore } from "react";

// Per-version dismissal of the sidebar update notice, persisted in localStorage.
// Backed by useSyncExternalStore so every mount re-renders when the value changes
// (a dismiss, an external clear from "Check for updates", or another tab).
export const DISMISS_KEY = "dockbrr_dismissed_update";
const CHANGED_EVENT = "dockbrr:dismiss-changed";

function subscribe(cb: () => void): () => void {
  window.addEventListener(CHANGED_EVENT, cb);
  window.addEventListener("storage", cb);
  return () => {
    window.removeEventListener(CHANGED_EVENT, cb);
    window.removeEventListener("storage", cb);
  };
}

function getSnapshot(): string | null {
  return localStorage.getItem(DISMISS_KEY);
}

export function useDismissedUpdate() {
  const dismissed = useSyncExternalStore(subscribe, getSnapshot);
  const dismiss = (latest: string) => {
    localStorage.setItem(DISMISS_KEY, latest);
    window.dispatchEvent(new Event(CHANGED_EVENT));
  };
  return { dismissed, dismiss };
}

// clearDismissedUpdate un-hides the notice (used by the manual "Check for
// updates" action so a previously dismissed update reappears).
export function clearDismissedUpdate(): void {
  localStorage.removeItem(DISMISS_KEY);
  window.dispatchEvent(new Event(CHANGED_EVENT));
}
