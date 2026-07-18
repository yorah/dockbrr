import { toast } from "sonner";

export const TOAST_DURATION_MS = 5000;

type Kind = "success" | "error" | "info";

// Sonner toasts auto-dismiss silently; suffix a live "(Ns)" countdown so the
// user can see how long the message sticks around. Re-issuing with the same id
// updates the toast in place (no re-animation); the remaining duration is
// passed on every tick so the dismiss timer and the label stay in step.
function show(kind: Kind, message: string, durationMs = TOAST_DURATION_MS) {
  const started = Date.now();
  const label = (left: number) => `${message} (${left}s)`;
  const id = toast[kind](label(Math.ceil(durationMs / 1000)), { duration: durationMs });
  const tick = setInterval(() => {
    const remaining = durationMs - (Date.now() - started);
    if (remaining <= 0) {
      clearInterval(tick);
      return;
    }
    toast[kind](label(Math.ceil(remaining / 1000)), { id, duration: remaining });
  }, 1000);
  return id;
}

export const notify = {
  success: (message: string, durationMs?: number) => show("success", message, durationMs),
  error: (message: string, durationMs?: number) => show("error", message, durationMs),
  info: (message: string, durationMs?: number) => show("info", message, durationMs),
};
